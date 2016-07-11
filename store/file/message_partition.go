package file

import (
	"encoding/binary"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/smancke/guble/store"

	log "github.com/Sirupsen/logrus"
)

var (
	MAGIC_NUMBER        = []byte{42, 249, 180, 108, 82, 75, 222, 182}
	FILE_FORMAT_VERSION = []byte{1}
	MESSAGES_PER_FILE   = uint64(10000)
	INDEX_ENTRY_SIZE    = 20
)

const (
	GubleNodeIdBits    = 3
	SequenceBits       = 12
	GubleNodeIdShift   = SequenceBits
	TimestampLeftShift = SequenceBits + GubleNodeIdBits
	GubleEpoch         = 1467714505012
)

type Index struct {
	messageID uint64
	offset    uint64
	size      uint32
	fileID    int
}

type MessagePartition struct {
	basedir             string
	name                string
	appendFile          *os.File
	indexFile           *os.File
	appendFilePosition  uint64
	maxMessageId        uint64
	localSequenceNumber uint64

	entriesCount uint64 //TODO  MAYBE USE ONLY ONE  FROM THE noOfEntriesInIndexFile AND localSequenceNumber
	mutex        *sync.RWMutex
	list         *IndexList
	fileCache    *cache
}

func NewMessagePartition(basedir string, storeName string) (*MessagePartition, error) {
	p := &MessagePartition{
		basedir:   basedir,
		name:      storeName,
		mutex:     &sync.RWMutex{},
		list:      newList(int(MESSAGES_PER_FILE)),
		fileCache: newCache(),
	}
	return p, p.initialize()
}

func (p *MessagePartition) initialize() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	err := p.readIdxFiles()
	if err != nil {
		messageStoreLogger.WithField("err", err).Error("MessagePartition error on scanFiles")
		return err
	}

	return nil
}

// Returns the start messages ids for all available message files
// in a sorted list
func (p *MessagePartition) readIdxFiles() error {
	allFiles, err := ioutil.ReadDir(p.basedir)
	if err != nil {
		return err
	}

	indexFilesName := make([]string, 0)
	for _, fileInfo := range allFiles {
		if strings.HasPrefix(fileInfo.Name(), p.name+"-") && strings.HasSuffix(fileInfo.Name(), ".idx") {
			fileIdString := filepath.Join(p.basedir, fileInfo.Name())
			messageStoreLogger.WithField("IDXname", fileIdString).Info("IDX NAME")
			indexFilesName = append(indexFilesName, fileIdString)
		}
	}
	// if no .idx file are found.. there is nothing to load
	if len(indexFilesName) == 0 {
		messageStoreLogger.Info("No .idx files found")
		return nil
	}

	//load the filecache from all the files
	messageStoreLogger.WithField("filenames", indexFilesName).Info("Found files")
	for i := 0; i < len(indexFilesName)-1; i++ {
		min, max, err := readMinMaxMsgIdFromIndexFile(indexFilesName[i])
		if err != nil {
			messageStoreLogger.WithFields(log.Fields{
				"idxFilename": indexFilesName[i],
				"err":         err,
			}).Error("Error loading existing .idxFile")
			return err
		}

		// check the message id's for max value
		if max >= p.maxMessageId {
			p.maxMessageId = max
		}

		// put entry in file cache
		p.fileCache.Append(&cacheEntry{
			min: min,
			max: max,
		})

	}
	// read the  idx file with   biggest id and load in the sorted cache
	err = p.lostLastIdxFile(indexFilesName[len(indexFilesName)-1])
	if err != nil {
		messageStoreLogger.WithFields(log.Fields{
			"idxFilename": indexFilesName[(len(indexFilesName) - 1)],
			"err":         err,
		}).Error("Error loading last .idx file")
		return err
	}

	if p.list.Back().messageID >= p.maxMessageId {
		p.maxMessageId = p.list.Back().messageID
	}

	return nil
}

func (p *MessagePartition) MaxMessageId() (uint64, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.maxMessageId, nil
}

func (p *MessagePartition) closeAppendFiles() error {
	if p.appendFile != nil {
		if err := p.appendFile.Close(); err != nil {
			if p.indexFile != nil {
				defer p.indexFile.Close()
			}
			return err
		}
		p.appendFile = nil
	}

	if p.indexFile != nil {
		err := p.indexFile.Close()
		p.indexFile = nil
		return err
	}
	return nil
}

// readMinMaxMsgIdFromIndexFile   reads the first and last entry from a idx file which should be sorted
func readMinMaxMsgIdFromIndexFile(filename string) (minMsgID, maxMsgID uint64, err error) {

	entriesInIndex, err := calculateNumberOfEntries(filename)
	if err != nil {
		return
	}

	file, err := os.Open(filename)
	defer file.Close()
	if err != nil {
		return
	}

	minMsgID, _, _, err = readIndexEntry(file, 0)
	if err != nil {
		return
	}
	maxMsgID, _, _, err = readIndexEntry(file, int64((entriesInIndex-1)*uint64(INDEX_ENTRY_SIZE)))
	if err != nil {
		return
	}
	return minMsgID, maxMsgID, err
}

func (p *MessagePartition) createNextAppendFiles() error {
	messageStoreLogger.WithField("newFilename", p.composeMsgFilename()).Info("CreateNextIndexApppendFIles")

	appendfile, err := os.OpenFile(p.composeMsgFilename(), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return err
	}

	// write file header on new files
	if stat, _ := appendfile.Stat(); stat.Size() == 0 {
		p.appendFilePosition = uint64(stat.Size())

		_, err = appendfile.Write(MAGIC_NUMBER)
		if err != nil {
			return err
		}

		_, err = appendfile.Write(FILE_FORMAT_VERSION)
		if err != nil {
			return err
		}
	}

	indexfile, errIndex := os.OpenFile(p.composeIndexFilename(), os.O_RDWR|os.O_CREATE, 0666)
	if errIndex != nil {
		defer appendfile.Close()
		defer os.Remove(appendfile.Name())
		return err
	}

	p.appendFile = appendfile
	p.indexFile = indexfile
	stat, err := appendfile.Stat()
	if err != nil {
		return err
	}
	p.appendFilePosition = uint64(stat.Size())

	return nil
}

func (p *MessagePartition) generateNextMsgId(nodeId int) (uint64, int64, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	//Get the local Timestamp
	currTime := time.Now()
	// timestamp in Seconds will be return to client
	timestamp := currTime.Unix()

	//Use the unixNanoTimestamp for generating id
	nanoTimestamp := currTime.UnixNano()

	if nanoTimestamp < GubleEpoch {
		err := fmt.Errorf("Clock is moving backwards. Rejecting requests until %d.", timestamp)
		return 0, 0, err
	}

	id := (uint64(nanoTimestamp-GubleEpoch) << TimestampLeftShift) |
		(uint64(nodeId) << GubleNodeIdShift) | p.localSequenceNumber

	p.localSequenceNumber++

	messageStoreLogger.WithFields(log.Fields{
		"id":                  id,
		"messagePartition":    p.basedir,
		"localSequenceNumber": p.localSequenceNumber,
		"currentNode":         nodeId,
	}).Info("Generated id")

	return id, timestamp, nil
}

func (p *MessagePartition) Close() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.closeAppendFiles()
}

func (p *MessagePartition) DoInTx(fnToExecute func(maxMessageId uint64) error) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	return fnToExecute(p.maxMessageId)
}

func (p *MessagePartition) Store(msgId uint64, msg []byte) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.store(msgId, msg)
}

func (p *MessagePartition) store(msgId uint64, msg []byte) error {

	if p.entriesCount == MESSAGES_PER_FILE ||
		p.appendFile == nil ||
		p.indexFile == nil {

		messageStoreLogger.WithFields(log.Fields{
			"msgId":         msgId,
			"p.noOfEntriew": p.entriesCount,
			"p.appendfile":  p.appendFile,
			"p.indexFile":   p.indexFile,
			"fileCache":     p.fileCache,
		}).Debug("iN Store")

		if err := p.closeAppendFiles(); err != nil {
			return err
		}

		if p.entriesCount == MESSAGES_PER_FILE {

			messageStoreLogger.WithFields(log.Fields{
				"msgId":         msgId,
				"p.noOfEntriew": p.entriesCount,
				"fileCache":     p.fileCache,
			}).Info("DUumping current file ")

			//sort the indexFile
			err := p.rewriteSortedIdxFile(p.composeIndexFilename())
			if err != nil {
				messageStoreLogger.WithField("err", err).Error("Error dumping file")
				return err
			}
			//Add items in the filecache
			p.fileCache.Append(&cacheEntry{
				min: p.list.Front().messageID,
				max: p.list.Back().messageID,
			})

			//clear the current sorted cache
			p.list.Clear()
			p.entriesCount = 0
		}

		if err := p.createNextAppendFiles(); err != nil {
			return err
		}
	}

	// write the message size and the message id: 32 bit and 64 bit, so 12 bytes
	sizeAndId := make([]byte, 12)
	binary.LittleEndian.PutUint32(sizeAndId, uint32(len(msg)))
	binary.LittleEndian.PutUint64(sizeAndId[4:], msgId)
	if _, err := p.appendFile.Write(sizeAndId); err != nil {
		return err
	}

	// write the message
	if _, err := p.appendFile.Write(msg); err != nil {
		return err
	}

	// write the index entry to the index file
	messageOffset := p.appendFilePosition + uint64(len(sizeAndId))
	err := writeIndexEntry(p.indexFile, msgId, messageOffset, uint32(len(msg)), p.entriesCount)
	if err != nil {
		return err
	}
	p.entriesCount++

	log.WithFields(log.Fields{
		"p.noOfEntriesInIndexFile": p.entriesCount,
		"msgID":                    msgId,
		"msgSize":                  uint32(len(msg)),
		"msgOffset":                messageOffset,
		"filename":                 p.indexFile.Name(),
	}).Debug("Wrote in indexFile")

	//create entry for l
	e := &Index{
		messageID: msgId,
		offset:    messageOffset,
		size:      uint32(len(msg)),
		fileID:    p.fileCache.Len(),
	}
	p.list.Insert(e)

	p.appendFilePosition += uint64(len(sizeAndId) + len(msg))

	if msgId >= msgId {
		p.maxMessageId = msgId
	}

	return nil
}

// Fetch fetches a set of messages
func (p *MessagePartition) Fetch(req *store.FetchRequest) {
	log.WithField("req", req.StartID).Debug("Fetching ")
	go func() {
		fetchList, err := p.calculateFetchList(req)

		if err != nil {
			log.WithField("err", err).Error("Error calculating list")
			req.ErrorC <- err
			return
		}

		log.WithField("fetchList", fetchList).Debug("Fetching")
		req.StartC <- fetchList.Len()

		log.WithField("fetchList", fetchList).Debug("Fetch 2")
		err = p.fetchByFetchlist(fetchList, req.MessageC)

		if err != nil {
			log.WithField("err", err).Error("Error calculating list")
			req.ErrorC <- err
			return
		}
		close(req.MessageC)
	}()
}

// fetchByFetchlist fetches the messages in the supplied fetchlist and sends them to the message-channel
func (p *MessagePartition) fetchByFetchlist(fetchList *IndexList, messageC chan store.MessageAndID) error {
	for _, f := range *fetchList {
		file, err := p.checkoutMessagefile(uint64(f.fileID))

		msg := make([]byte, f.size, f.size)
		_, err = file.ReadAt(msg, int64(f.offset))
		if err != nil {
			messageStoreLogger.WithFields(log.Fields{
				"err":    err,
				"offset": f.offset,
			}).Error("Error ReadAt")
			file.Close()
			return err
		}
		messageC <- store.MessageAndID{f.messageID, msg}
		file.Close()
	}
	return nil
}

func retrieveFromList(l *IndexList, req *store.FetchRequest) *IndexList {
	potentialEntries := newList(0)
	found, pos, lastPos, _ := l.GetIndexEntryFromID(req.StartID)
	currentPos := lastPos
	if found == true {
		currentPos = pos
	}

	for potentialEntries.Len() < req.Count && currentPos >= 0 && currentPos < l.Len() {
		e := l.Get(currentPos)
		//e := (*l)[currentPos]
		if e == nil { //TODO investigate why nil is returned sometimes
			messageStoreLogger.WithFields(log.Fields{
				"pos":     currentPos,
				"l.Len":   l.Len(),
				"len":     potentialEntries.Len(),
				"startID": req.StartID,
				"count":   req.Count,
				"e":       e,
				"e.msgId": (*l)[currentPos],
			}).Error("Error in retrieving from list.Got nil entry")
			break
		}
		potentialEntries.Insert(e)
		currentPos += req.Direction
	}
	return potentialEntries
}

// calculateFetchList returns a list of fetchEntry records for all messages in the fetch request.
func (p *MessagePartition) calculateFetchList(req *store.FetchRequest) (*IndexList, error) {
	if req.Direction == 0 {
		req.Direction = 1
	}

	potentialEntries := newList(0)

	//reading from IndexFiles
	// TODO: fix  prev when EndID logic will be done
	prev := false
	p.fileCache.cacheMutex.RLock()

	for i, fce := range p.fileCache.entries {
		if fce.has(req) || (prev && potentialEntries.Len() < req.Count) {
			prev = true

			l, err := loadIdxFile(p.composeIndexFilenameWithValue(uint64(i)), i)
			if err != nil {
				messageStoreLogger.WithError(err).Info("Error loading idx file in memory")
				return nil, err
			}

			currentEntries := retrieveFromList(l, req)
			potentialEntries.InsertList(currentEntries)
		} else {
			prev = false
		}
	}

	//read from current cached value (the idx file which size is smaller than MESSAGE_PER_FILE

	fce := fileCacheEntryForList(p.list)
	if fce.has(req) || (prev && potentialEntries.Len() < req.Count) {
		currentEntries := retrieveFromList(p.list, req)
		potentialEntries.InsertList(currentEntries)
	}
	//Currently potentialEntries contains a potentials msgIDs from any files and from inMemory.From this will select only Count Id.
	fetchList := retrieveFromList(potentialEntries, req)

	p.fileCache.cacheMutex.RUnlock()
	return fetchList, nil
}

func (p *MessagePartition) rewriteSortedIdxFile(filename string) error {
	messageStoreLogger.WithFields(log.Fields{
		"filename": filename,
	}).Info("Dumping Sorted list")

	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0666)
	defer file.Close()
	if err != nil {
		return err
	}

	lastMsgID := uint64(0)
	for i := 0; i < p.list.Len(); i++ {
		item := p.list.Get(i)

		if lastMsgID >= item.messageID {
			messageStoreLogger.WithFields(log.Fields{
				"err":      err,
				"filename": filename,
			}).Error("Sorted list is not sorted")

			return err
		}
		lastMsgID = item.messageID
		err := writeIndexEntry(file, item.messageID, item.offset, item.size, uint64(i))
		messageStoreLogger.WithFields(log.Fields{
			"curMsgId": item.messageID,
			"err":      err,
			"pos":      i,
			"filename": file.Name(),
		}).Debug("Wrote while dumpSortedIndexFile")

		if err != nil {
			messageStoreLogger.WithField("err", err).Error("Error writing indexfile in sorted way.")
			return err
		}
	}
	return nil

}

// readIndexEntry reads from a .idx file from the given `indexPosition` the msgIDm msgOffset and msgSize
func readIndexEntry(file *os.File, indexPosition int64) (uint64, uint64, uint32, error) {
	msgOffsetBuff := make([]byte, INDEX_ENTRY_SIZE)
	if _, err := file.ReadAt(msgOffsetBuff, indexPosition); err != nil {
		messageStoreLogger.WithFields(log.Fields{
			"err":      err,
			"file":     file.Name(),
			"indexPos": indexPosition,
		}).Error("ReadIndexEntry failed ")
		return 0, 0, 0, err
	}

	msgId := binary.LittleEndian.Uint64(msgOffsetBuff)
	msgOffset := binary.LittleEndian.Uint64(msgOffsetBuff[8:])
	msgSize := binary.LittleEndian.Uint32(msgOffsetBuff[16:])
	return msgId, msgOffset, msgSize, nil
}

// writeIndexEntry write in a .idx file to  the given `pos` the msgIDm msgOffset and msgSize
func writeIndexEntry(file *os.File, msgID uint64, messageOffset uint64, msgSize uint32, pos uint64) error {
	indexPosition := int64(uint64(INDEX_ENTRY_SIZE) * pos)
	messageOffsetBuff := make([]byte, INDEX_ENTRY_SIZE)

	binary.LittleEndian.PutUint64(messageOffsetBuff, msgID)
	binary.LittleEndian.PutUint64(messageOffsetBuff[8:], messageOffset)
	binary.LittleEndian.PutUint32(messageOffsetBuff[16:], msgSize)

	if _, err := file.WriteAt(messageOffsetBuff, indexPosition); err != nil {
		messageStoreLogger.WithFields(log.Fields{
			"err":           err,
			"indexPosition": indexPosition,
			"msgID":         msgID,
		}).Error("ERROR writeIndexEntry")
		return err
	}
	return nil
}

// calculateNumberOfEntries reads the idx file with name `filename` and will calculate how many entries are
func calculateNumberOfEntries(filename string) (uint64, error) {
	stat, err := os.Stat(filename)
	if err != nil {
		messageStoreLogger.WithField("err", err).Error("Stat failed")
		return 0, err
	}
	entriesInIndex := uint64(stat.Size() / int64(INDEX_ENTRY_SIZE))
	return entriesInIndex, nil
}

// loadLastIndexFile will construct the current Sorted List for fetch entries which corresponds to the idx file with the biggest name
func (p *MessagePartition) lostLastIdxFile(filename string) error {
	messageStoreLogger.WithField("filename", filename).Info("loadIndexFileInMemory")

	l, err := loadIdxFile(filename, p.fileCache.Len())
	if err != nil {
		messageStoreLogger.WithError(err).Error("Error loading filename")
		return err
	}

	p.list = l
	p.entriesCount = uint64(l.Len())

	return nil
}

// loadIndexFile will read a file and will return a sorted list for fetchEntries
func loadIdxFile(filename string, fileID int) (*IndexList, error) {
	l := newList(int(MESSAGES_PER_FILE))
	messageStoreLogger.WithField("filename", filename).Debug("loadIndexFile")

	entriesInIndex, err := calculateNumberOfEntries(filename)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(filename)
	defer file.Close()
	if err != nil {
		messageStoreLogger.WithField("err", err).Error("os.Open failed")
		return nil, err
	}

	for i := uint64(0); i < entriesInIndex; i++ {
		msgID, msgOffset, msgSize, err := readIndexEntry(file, int64(i*uint64(INDEX_ENTRY_SIZE)))
		messageStoreLogger.WithFields(log.Fields{
			"msgOffset": msgOffset,
			"msgSize":   msgSize,
			"msgID":     msgID,
			"err":       err,
		}).Debug("readIndexEntry")

		if err != nil {
			log.WithField("err", err).Error("Read error")
			return nil, err
		}

		e := &Index{
			size:      msgSize,
			messageID: msgID,
			offset:    msgOffset,
			fileID:    fileID,
		}
		l.Insert(e)
		messageStoreLogger.WithField("lenl", l.Len()).Debug("loadIndexFile")
	}
	return l, nil
}

// checkoutMessagefile returns a file handle to the message file with the supplied file id. The returned file handle may be shared for multiple go routines.
func (p *MessagePartition) checkoutMessagefile(fileId uint64) (*os.File, error) {
	return os.Open(p.composeMsgFilenameWithValue(fileId))
}

//// releaseMessagefile releases a message file handle
//func (p *MessagePartition) releaseMessagefile(fileId uint64, file *os.File) {
//	file.Close()
//}
//
//// checkoutIndexfile returns a file handle to the index file with the supplied file id. The returned file handle may be shared for multiple go routines.
//func (p *MessagePartition) checkoutIndexfile() (*os.File, error) {
//	return os.Open(p.composeIndexFilename())
//}
//
//// releaseIndexfile releases an index file handle
//func (p *MessagePartition) releaseIndexfile(fileId uint64, file *os.File) {
//	file.Close()
//}

func (p *MessagePartition) composeMsgFilename() string {
	return filepath.Join(p.basedir, fmt.Sprintf("%s-%020d.msg", p.name, uint64(p.fileCache.Len())))
}

func (p *MessagePartition) composeMsgFilenameWithValue(value uint64) string {
	return filepath.Join(p.basedir, fmt.Sprintf("%s-%020d.msg", p.name, value))
}

func (p *MessagePartition) composeIndexFilename() string {
	return filepath.Join(p.basedir, fmt.Sprintf("%s-%020d.idx", p.name, uint64(p.fileCache.Len())))
}

func (p *MessagePartition) composeIndexFilenameWithValue(value uint64) string {
	return filepath.Join(p.basedir, fmt.Sprintf("%s-%020d.idx", p.name, value))
}

func fileCacheEntryForList(l *IndexList) (entry cacheEntry) {
	front, back := l.Front(), l.Back()
	if front != nil {
		entry.min = front.messageID
	}
	if back != nil {
		entry.max = back.messageID
	}
	return
}
