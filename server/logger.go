package server

import (
	log "github.com/Sirupsen/logrus"
)

var logger = log.WithFields(log.Fields{
	"module": "server",
})
