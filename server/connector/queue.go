package connector

import (
	"sync"

	"errors"

	log "github.com/Sirupsen/logrus"
)

var (
	ErrQueueResponseHandler = errors.New("Queue should have a ResponseHandler")
)

// Queue is an interface modeling a task-queue (it is started and more Requests can be pushed to it, an finally it is stopped).
type Queue interface {
	ResponseHandleSetter

	Start() error
	Push(request Request) error
	Stop() error
}

type queue struct {
	sender    Sender
	handler   ResponseHandler
	requestsC chan Request
	nWorkers  int
	wg        sync.WaitGroup
}

// NewQueue returns a new Queue (not started).
func NewQueue(sender Sender, nWorkers int) Queue {
	q := &queue{
		sender:    sender,
		requestsC: make(chan Request),
		nWorkers:  nWorkers,
	}
	return q
}

func (q *queue) SetResponseHandler(rh ResponseHandler) {
	q.handler = rh
}

func (q *queue) ResponseHandler() ResponseHandler {
	return q.handler
}

func (q *queue) Start() error {
	if q.handler == nil {
		logger.Warning("Not handler set for connector queue.")
	}
	for i := 1; i <= q.nWorkers; i++ {
		go q.worker()
	}
	return nil
}

func (q *queue) worker() {
	for request := range q.requestsC {
		q.wg.Add(1)
		response, err := q.sender.Send(request)
		if q.handler != nil {
			err = q.handler.HandleResponse(request, response, err)
			if err != nil {
				logger.WithFields(log.Fields{
					"error":      err.Error(),
					"subscriber": request.Subscriber(),
					"message":    request.Message(),
				}).Error("Error handling connector response")
			}
		}
		q.wg.Done()
	}
}

func (q *queue) Push(request Request) error {
	q.requestsC <- request
	return nil
}

func (q *queue) Stop() error {
	close(q.requestsC)
	q.wg.Wait()
	return nil
}