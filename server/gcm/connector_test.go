package gcm

import (
	"github.com/Bogh/gcm"
	"github.com/smancke/guble/protocol"
	"github.com/smancke/guble/server/kvstore"
	"github.com/smancke/guble/server/router"
	"github.com/smancke/guble/testutil"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConnector_ServeHTTPSuccess(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, routerMock, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/notifications", string(route.Path))
		a.Equal("marvin", route.Get(userIDKey))
		a.Equal("gcmId123", route.Get(applicationIDKey))
	})

	time.Sleep(100 * time.Millisecond)

	postSubscription(t, gcm, "marvin", "gcmId123", "notifications")

}

func TestConnector_ServeHTTPWithErrorCases(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, _, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	u, _ := url.Parse("http://localhost/gcm/marvin/gcmId123/subscribe/notifications")
	// and a http context
	req := &http.Request{URL: u, Method: "GET"}
	w := httptest.NewRecorder()

	// do a GET instead of POST
	gcm.ServeHTTP(w, req)

	// check the result
	a.Equal("Method not allowed\n", string(w.Body.Bytes()))
	a.Equal(w.Code, http.StatusMethodNotAllowed)

	// send a new request with wrong parameters encoding
	req.Method = "POST"
	req.URL, _ = u.Parse("http://localhost/gcm/marvin/gcmId123/subscribe3/notifications")

	w2 := httptest.NewRecorder()
	gcm.ServeHTTP(w2, req)

	a.Equal("Invalid Parameters in request\n", string(w2.Body.Bytes()))
	a.Equal(w2.Code, http.StatusBadRequest)
}

func TestConnector_Check(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, _, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	done := make(chan bool)
	mockSender := testutil.CreateGcmSender(
		testutil.CreateRoundTripperWithJsonResponse(
			http.StatusOK, testutil.SuccessGCMResponse, done))
	gcm.Sender = mockSender
	err := gcm.Check()
	a.NoError(err)

	done2 := make(chan bool)
	mockSender2 := testutil.CreateGcmSender(
		testutil.CreateRoundTripperWithJsonResponse(
			http.StatusUnauthorized, "", done2))
	gcm.Sender = mockSender2
	a.Error(gcm.Check())
}

func TestGCM_SaveAndLoadSubs(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, routerMock, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	// given: some test routes
	testRoutes := map[string]bool{
		"marvin:/foo:1234": true,
		"zappod:/bar:1212": true,
		"athur:/erde:42":   true,
	}

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		// delete the route from the map, if we got it in the test
		delete(testRoutes, fmt.Sprintf("%v:%v:%v", route.Get(userIDKey), route.Path, route.Get(applicationIDKey)))
	}).AnyTimes()

	// when: we save the routes
	for k := range testRoutes {
		splitKey := strings.SplitN(k, ":", 3)
		userID := splitKey[0]
		topic := splitKey[1]
		gcmID := splitKey[2]
		initSubscription(gcm, topic, userID, gcmID, 0, true)
	}

	// and reload the routes
	gcm.loadSubscriptions()

	time.Sleep(50 * time.Millisecond)

	// then: all expected subscriptions were called
	a.Equal(0, len(testRoutes))
}

func TestRemoveTrailingSlash(t *testing.T) {
	assert.Equal(t, "/foo", removeTrailingSlash("/foo/"))
	assert.Equal(t, "/foo", removeTrailingSlash("/foo"))
}

func TestConnector_parseParams(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, _, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	testCases := []struct {
		urlPath, userID, gcmID, topic, err string
	}{
		{"/gcm/marvin/gcmId123/subscribe/notifications", "marvin", "gcmId123", "/notifications", ""},
		{"/gcm2/marvin/gcmId123/subscribe/notifications", "", "", "", "gcm: GCM request is not starting with gcm prefix"},
		{"/gcm/marvin/gcmId123/subscrib2e/notifications", "", "", "", "gcm: GCM request third param is not subscribe"},
		{"/gcm/marvin/gcmId123subscribenotifications", "", "", "", "gcm: GCM request has wrong number of params"},
		{"/gcm/marvin/gcmId123/subscribe/notifications/alert/", "marvin", "gcmId123", "/notifications/alert", ""},
	}

	for i, c := range testCases {
		userID, gcmID, topic, err := gcm.parseParams(c.urlPath)

		//if error message is present check only the error
		if c.err != "" {
			a.NotNil(err)
			a.EqualError(err, c.err, fmt.Sprintf("Failed on testcase no=%d", i))
		} else {
			a.Equal(userID, c.userID, fmt.Sprintf("Failed on testcase no=%d", i))
			a.Equal(gcmID, c.gcmID, fmt.Sprintf("Failed on testcase no=%d", i))
			a.Equal(topic, c.topic, fmt.Sprintf("Failed on testcase no=%d", i))
			a.Nil(err, fmt.Sprintf("Failed on testcase no=%d", i))
		}
	}
	err := gcm.Stop()
	a.Nil(err)
}

func TestConnector_GetPrefix(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, _, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	a.Equal(gcm.GetPrefix(), "/gcm/")
}

func TestConnector_Stop(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, _, _ := testGCMResponse(t, testutil.SuccessGCMResponse)

	err := gcm.Stop()
	a.Nil(err)
	a.Equal(len(gcm.stopC), 0, "The Stop Channel should be empty")
	select {
	case _, opened := <-gcm.stopC:
		a.False(opened, "The Stop Channel should be closed")
	default:
		a.Fail("Reading from the Stop Channel should not block")
	}
}

func TestConnector_StartWithMessageSending(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, _, done := testGCMResponse(t, testutil.SuccessGCMResponse)

	// put a dummy gcm message with minimum information
	route := &router.Route{
		RouteConfig: router.RouteConfig{
			RouteParams: router.RouteParams{applicationIDKey: "id"},
		},
	}
	s := newSubscription(gcm, route, 0)

	msgWithNoRecipients := newPipeMessage(s, &protocol.Message{
		ID:   uint64(4),
		Body: []byte("{id:id}"),
		Time: 1405544146,
		Path: "/gcm/marvin/gcm124/subscribe/stuff"})

	gcm.pipelineC <- msgWithNoRecipients
	// expect that the Http Server to give us a malformed message
	<-done

	//wait a little to Stop the GcmConnector
	time.Sleep(50 * time.Millisecond)
	err := gcm.Stop()
	a.NoError(err)
}

func TestConnector_GetErrorMessageFromGCM(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, routerMock, done := testGCMResponse(t, testutil.ErrorGCMResponse)

	// expect the route unsubscribed from removeSubscription
	routerMock.EXPECT().Unsubscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/path", string(route.Path))
		a.Equal("id", route.Get(applicationIDKey))
	})

	// expect the route subscribe with the new canonicalId from replaceSubscriptionWithCanonicalID
	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/path", string(route.Path))
		a.Equal("marvin", route.Get(userIDKey))
		a.Equal("id", route.Get(applicationIDKey))
	})
	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/path", string(route.Path))
		a.Equal("marvin", route.Get(userIDKey))
		appid := route.Get(applicationIDKey)
		a.Equal("gcmCanonicalID", appid)
	})

	// Wait for subscriptions to finish loading
	time.Sleep(100 * time.Millisecond)

	// put a dummy gcm message with minimum information
	s, err := initSubscription(gcm, "/path", "marvin", "id", 0, true)
	a.NoError(err)
	time.Sleep(100 * time.Millisecond)

	message := &protocol.Message{
		ID:   uint64(4),
		Body: []byte("{id:id}"),
		Time: 1405544146,
		Path: "/gcm/marvin/gcm124/subscribe/stuff",
	}

	// send the message into the subscription route channel
	s.route.Deliver(message)
	// expect that the Http Server gives us a malformed message
	<-done

	//wait before closing the gcm connector
	time.Sleep(500 * time.Millisecond)

	// stop the channel of the subscription
	// s.route.Close()

	err = gcm.Stop()
	a.NoError(err)
}

func TestConnector_Subscribe(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, routerMock, _ := testSimpleGCM(t, true)

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/baskets", string(route.Path))
		a.Equal("user1", route.Get(userIDKey))
		a.Equal("gcm1", route.Get(applicationIDKey))
	})

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/baskets", string(route.Path))
		a.Equal("user2", route.Get(userIDKey))
		a.Equal("gcm2", route.Get(applicationIDKey))
	})

	postSubscription(t, gcm, "user1", "gcm1", "baskets")
	a.Equal(len(gcm.subscriptions), 1)

	postSubscription(t, gcm, "user2", "gcm2", "baskets")
	a.Equal(len(gcm.subscriptions), 2)
}

func TestConnector_Unsubscribe(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)
	gcm, routerMock, _ := testSimpleGCM(t, true)
	var deletedRoute *router.Route

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		deletedRoute = route
		a.Equal("/baskets", string(route.Path))
		a.Equal("user1", route.Get(userIDKey))
		a.Equal("gcm1", route.Get(applicationIDKey))

	})

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) {
		a.Equal("/baskets", string(route.Path))
		a.Equal("user2", route.Get(userIDKey))
		a.Equal("gcm2", route.Get(applicationIDKey))
	})

	postSubscription(t, gcm, "user1", "gcm1", "baskets")
	a.Equal(len(gcm.subscriptions), 1)

	postSubscription(t, gcm, "user2", "gcm2", "baskets")
	a.Equal(len(gcm.subscriptions), 2)

	routerMock.EXPECT().Unsubscribe(deletedRoute)

	deleteSubscription(t, gcm, "user1", "gcm1", "baskets")
	a.Equal(len(gcm.subscriptions), 1)

	remainingKey := composeSubscriptionKey("/baskets", "user2", "gcm2")

	a.Equal("user2", gcm.subscriptions[remainingKey].route.Get(userIDKey))
	a.Equal("gcm2", gcm.subscriptions[remainingKey].route.Get(applicationIDKey))
	a.Equal("/baskets", string(gcm.subscriptions[remainingKey].route.Path))
}

func TestConnector_SubscriptionExists(t *testing.T) {
	_, finish := testutil.NewMockCtrl(t)
	defer finish()
	a := assert.New(t)

	gcm, routerMock, _ := testSimpleGCM(t, true)

	routerMock.EXPECT().Subscribe(gomock.Any())

	w := httptest.NewRecorder()
	u := fmt.Sprintf("http://localhost/gcm/%s/%s/subscribe/%s", "user01", "gcm01", "/test")

	req, err := http.NewRequest(http.MethodPost, u, nil)
	a.NoError(err)

	gcm.ServeHTTP(w, req)
	w = httptest.NewRecorder()
	gcm.ServeHTTP(w, req)

	a.Equal(http.StatusOK, w.Code)
	a.Equal("subscription exists", w.Body.String())
}

func TestGCM_FCMFormatMessage(t *testing.T) {
	mockCtrl, finish := testutil.NewMockCtrl(t)
	defer finish()

	a := assert.New(t)

	var subRoute *router.Route

	connector, routerMock, _ := testSimpleGCM(t, false)
	gcmSenderMock := NewMockSender(mockCtrl)
	connector.Sender = gcmSenderMock
	connector.Start()
	defer connector.Stop()
	time.Sleep(50 * time.Millisecond)

	routerMock.EXPECT().Subscribe(gomock.Any()).Do(func(route *router.Route) (*router.Route, error) {
		subRoute = route
		return route, nil
	})

	postSubscription(t, connector, "user01", "device01", "topic")

	// send a fully formated GCM message
	m := &protocol.Message{
		Path: "/topic",
		ID:   1,
		Body: []byte(fullFCMMessage),
	}

	if !a.NotNil(subRoute) {
		return
	}

	doneC := make(chan bool)

	gcmSenderMock.EXPECT().Send(gomock.Any()).Do(func(m *gcm.Message) (*gcm.Response, error) {
		a.NotNil(m.Notification)
		a.Equal("TEST", m.Notification.Title)
		a.Equal("notification body", m.Notification.Body)
		a.Equal("ic_notification_test_icon", m.Notification.Icon)
		a.Equal("estimated_arrival", m.Notification.ClickAction)

		a.NotNil(m.Data)
		if a.Contains(m.Data, "field1") {
			a.Equal("value1", m.Data["field1"])
		}
		if a.Contains(m.Data, "field2") {
			a.Equal("value2", m.Data["field2"])
		}

		doneC <- true
		return nil, nil
	}).Return(&gcm.Response{}, nil)

	subRoute.Deliver(m)
	select {
	case <-doneC:
	case <-time.After(100 * time.Millisecond):
		a.Fail("Message not received by FCM")
	}

	m = &protocol.Message{
		Path: "/topic",
		ID:   1,
		Body: []byte(`plain body`),
	}

	gcmSenderMock.EXPECT().Send(gomock.Any()).Do(func(m *gcm.Message) (*gcm.Response, error) {
		a.Nil(m.Notification)

		a.NotNil(m.Data)
		a.Contains(m.Data, "message")

		doneC <- true
		return nil, nil
	}).Return(&gcm.Response{}, nil)

	subRoute.Deliver(m)
	select {
	case <-doneC:
	case <-time.After(100 * time.Millisecond):
		a.Fail("Message not received by FCM")
	}
}

func testGCMResponse(t *testing.T, jsonResponse string) (*Connector, *MockRouter, chan bool) {
	gcm, routerMock, _ := testSimpleGCM(t, false)

	err := gcm.Start()
	assert.NoError(t, err)

	done := make(chan bool)
	// return err
	mockSender := testutil.CreateGcmSender(
		testutil.CreateRoundTripperWithJsonResponse(http.StatusOK, jsonResponse, done))
	gcm.Sender = mockSender
	return gcm, routerMock, done
}

func testSimpleGCM(t *testing.T, mockStore bool) (*Connector, *MockRouter, *MockMessageStore) {
	routerMock := NewMockRouter(testutil.MockCtrl)
	routerMock.EXPECT().Cluster().Return(nil)
	kvStore := kvstore.NewMemoryKVStore()
	routerMock.EXPECT().KVStore().Return(kvStore, nil)

	gcm, err := New(routerMock, "/gcm/", "testApi", 1, "")
	assert.NoError(t, err)

	var storeMock *MockMessageStore
	if mockStore {
		storeMock = NewMockMessageStore(testutil.MockCtrl)
		routerMock.EXPECT().MessageStore().Return(storeMock, nil).AnyTimes()
	}

	return gcm, routerMock, storeMock
}

func postSubscription(t *testing.T, gcm *Connector, userID, gcmID, topic string) {
	a := assert.New(t)
	u := fmt.Sprintf("http://localhost/gcm/%s/%s/subscribe/%s", userID, gcmID, topic)
	req, err := http.NewRequest(http.MethodPost, u, nil)
	a.NoError(err)
	w := httptest.NewRecorder()

	gcm.ServeHTTP(w, req)

	a.Equal(fmt.Sprintf("subscribed: /%s\n", topic), string(w.Body.Bytes()))
}

func deleteSubscription(t *testing.T, gcm *Connector, userID, gcmID, topic string) {
	a := assert.New(t)
	u := fmt.Sprintf("http://localhost/gcm/%s/%s/subscribe/%s", userID, gcmID, topic)
	req, err := http.NewRequest(http.MethodDelete, u, nil)
	a.NoError(err)
	w := httptest.NewRecorder()

	gcm.ServeHTTP(w, req)

	a.Equal(fmt.Sprintf("unsubscribed: /%s\n", topic), string(w.Body.Bytes()))
}