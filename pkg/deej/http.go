package deej

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// HttpIO provides a deej-aware abstraction layer to managing serial I/O over an HTTP connection
type HttpIO struct {
	SerialIO

	deej   *Deej
	logger *zap.SugaredLogger

	stopChannel chan bool
	connected   bool
	conn        io.ReadWriteCloser

	lastKnownNumSliders        int
	currentSliderPercentValues []float32

	sliderMoveConsumers []chan SliderMoveEvent
}

type serial_cmd_struct struct {
	command string
}

// NewHttpIO creates a HttpIO instance that uses the provided deej
// instance's connection info to establish communications with the arduino chip
func NewHttpIO(deej *Deej, logger *zap.SugaredLogger) (*HttpIO, error) {
	logger = logger.Named("http")

	hio := &HttpIO{
		deej:                deej,
		logger:              logger,
		stopChannel:         make(chan bool),
		connected:           false,
		conn:                nil,
		sliderMoveConsumers: []chan SliderMoveEvent{},
	}

	logger.Debug("Created HTTP i/o instance")

	// respond to config changes
	hio.setupOnConfigReload()

	return hio, nil
}

// Start attempts to start an http server
func (hio *HttpIO) Start() error {
	lineChannel := make(chan string)

	handleSerialCommand := func(c *gin.Context) {
		data, err := c.GetRawData()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid data"})
		}
		command := string(data)
		lineChannel <- command
		c.IndentedJSON(http.StatusCreated, fmt.Sprintf("Accepted serial command %s", command))
	}

	go func() {
		router := gin.Default()
		hio.logger.Info("Starting server at localhost:6332/serial")
		router.POST("/serial", handleSerialCommand)
		if err := router.Run("localhost:6332"); err != nil {
			hio.Stop()
		}
	}()

	// read lines or await a stop
	go func() {
		for {
			select {
			case <-hio.stopChannel:
				hio.close(hio.logger)
			case line := <-lineChannel:
				hio.handleLine(hio.logger, line)
			}
		}
	}()

	return nil
}

// Stop signals us to shut down our Http connection, if one is active
func (hio *HttpIO) Stop() {
	if hio.connected {
		hio.logger.Debug("Shutting down Http connection")
		hio.stopChannel <- true
	} else {
		hio.logger.Debug("Not currently connected, nothing to stop")
	}
}

// SubscribeToSliderMoveEvents returns an unbuffered channel that receives
// a sliderMoveEvent struct every time a slider moves
func (hio *HttpIO) SubscribeToSliderMoveEvents() chan SliderMoveEvent {
	ch := make(chan SliderMoveEvent)
	hio.sliderMoveConsumers = append(hio.sliderMoveConsumers, ch)

	return ch
}

func (hio *HttpIO) setupOnConfigReload() {
	configReloadedChannel := hio.deej.config.SubscribeToChanges()

	const stopDelay = 50 * time.Millisecond

	go func() {
		for range configReloadedChannel {
			// make any config reload unset our slider number to ensure process volumes are being re-set
			// (the next read line will emit SliderMoveEvent instances for all sliders)\
			// this needs to happen after a small delay, because the session map will also re-acquire sessions
			// whenever the config file is reloaded, and we don't want it to receive these move events while the map
			// is still cleared. this is kind of ugly, but shouldn't cause any issues
			go func() {
				<-time.After(stopDelay)
				hio.lastKnownNumSliders = 0
			}()
		}
	}()
}

func (hio *HttpIO) close(logger *zap.SugaredLogger) {
	if err := hio.conn.Close(); err != nil {
		hio.logger.Warnw("Failed to close HTTP connection", "error", err)
	} else {
		hio.logger.Debug("HTTP connection closed")
	}

	hio.conn = nil
	hio.connected = false
}

func (hio *HttpIO) handleLine(logger *zap.SugaredLogger, line string) {
	sio := &SerialIO{
		deej:                hio.deej,
		logger:              hio.logger,
		stopChannel:         hio.stopChannel,
		connected:           hio.connected,
		conn:                hio.conn,
		sliderMoveConsumers: hio.sliderMoveConsumers,
	}
	sio.handleLine(logger, line+"\r\n")
}
