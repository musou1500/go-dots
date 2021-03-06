package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"strconv"

	dots_config "github.com/nttdots/go-dots/dots_client/config"
	log "github.com/sirupsen/logrus"
	"github.com/ugorji/go/codec"
	"github.com/nttdots/go-dots/dots_common"
	"github.com/nttdots/go-dots/dots_common/messages"
	"github.com/nttdots/go-dots/dots_client/task"
	"github.com/nttdots/go-dots/libcoap"
	"github.com/shopspring/decimal"
	client_message "github.com/nttdots/go-dots/dots_client/messages"
)

type RequestInterface interface {
	LoadJson([]byte) error
	CreateRequest()
	Send() Response
}

/*
 * Dots requests
 */
type Request struct {
	Message     interface{}
	RequestCode messages.Code
	pdu         *libcoap.Pdu
	coapType    libcoap.Type
	method      string
	requestName string
	queryParams []string

	env         *task.Env
	options     map[messages.Option]string
}

/*
 * Dots response
 */
type Response struct {
	StatusCode libcoap.Code
	data       []byte
}

/*
 * Request constructor.
 */
func NewRequest(code messages.Code, coapType libcoap.Type, method string, requestName string, queryParams []string, env *task.Env, options map[messages.Option]string) *Request {
	return &Request{
		nil,
		code,
		nil,
		coapType,
		method,
		requestName,
		queryParams,
		env,
		options,
	}
}

/*
 * Load a Message to this Request
 */
func (r *Request) LoadMessage(message interface{}) {
	r.Message = message
}

/*
 * convert this Request into the Cbor format.
 */
func (r *Request) dumpCbor() []byte {
	var buf []byte
	h := new(codec.CborHandle)
	e := codec.NewEncoderBytes(&buf, h)

	err := e.Encode(r.Message)
	if err != nil {
		log.Errorf("Error decoding %s", err)
	}
	return buf
}

/*
 * convert this Requests into the JSON format.
 */
func (r *Request) dumpJson() []byte {
	payload, _ := json.Marshal(r.Message)
	return payload
}

/*
 * Load Message from JSON data.
 */
func (r *Request) LoadJson(jsonData []byte) error {
	m := reflect.New(r.RequestCode.Type()).Interface()

	err := json.Unmarshal(jsonData, &m)
	if err != nil {
		return fmt.Errorf("Can't Convert Json to Message Object: %v\n", err)

	}
	r.Message = m
	return nil
}

/*
 * return the Request paths.
 */
func (r *Request) pathString() {
	r.RequestCode.PathString()
}

/*
 * Create CoAP requests.
 */
func (r *Request) CreateRequest() {
	var code libcoap.Code
	var observe uint16

	switch strings.ToUpper(r.method) {
	case "GET":
		code = libcoap.RequestGet
	case "PUT":
		code = libcoap.RequestPut
	case "POST":
		code = libcoap.RequestPost
	case "DELETE":
		code = libcoap.RequestDelete
	default:
		log.WithField("method", r.method).Error("invalid request method.")
	}

	r.pdu = &libcoap.Pdu{}
	r.pdu.Type = r.coapType
	r.pdu.Code = code
	r.pdu.MessageID = r.env.CoapSession().NewMessageID()
	r.pdu.Token = dots_common.RandStringBytes(8)
	r.pdu.Options = make([]libcoap.Option, 0)
	observeStr := r.options[messages.OBSERVE]
	if observeStr != "" {
		observeValue, err := strconv.ParseUint(observeStr, 10, 16)
		if err != nil {
			log.Errorf("Observe is not uint type.")
			goto SKIP_OBSERVE
		}
		observe = uint16(observeValue)

		if observe == uint16(messages.Register) || observe == uint16(messages.Deregister) {
			r.pdu.SetOption(libcoap.OptionObserve, observe)
			queryString := task.QueryParamsToString(r.queryParams)
			token, _ := r.env.GetTokenAndRequestQuery(queryString)

			// if observe is register, add request query with token as key and value (query = query of request, countMitigation = nil, isNotification = false)
			// if observe is deregister, remove query request
			if observe == uint16(messages.Register) {
				if token != nil {
					r.pdu.Token = token
				} else {
					reqQuery := task.RequestQuery{ queryString, nil }
					r.env.AddRequestQuery(string(r.pdu.Token), &reqQuery)
				}
			} else {
				if token != nil {
					r.pdu.Token = token
					r.env.RemoveRequestQuery(string(token))
				}
			}
		}
	}

SKIP_OBSERVE:
	if val, ok := r.options[messages.IFMATCH]; ok {
		r.pdu.SetOption(libcoap.OptionIfMatch, val)
	}

	// Block 2 option
	if (r.requestName == "mitigation_request") && (r.method == "GET") {
		blockSize := r.env.InitialRequestBlockSize()
		if blockSize != nil {
			block := &libcoap.Block{}
			block.NUM = 0
			block.M   = 0
			block.SZX = *blockSize
			r.pdu.SetOption(libcoap.OptionBlock2, uint32(block.ToInt()))
		} else {
			log.Debugf("Not set block 2 option")
		}
	}

	if r.Message != nil {
		r.pdu.Data = r.dumpCbor()
		r.pdu.SetOption(libcoap.OptionContentFormat, uint16(libcoap.AppCbor))
		log.Debugf("hex dump cbor request:\n%s", hex.Dump(r.pdu.Data))
	}
	tmpPathWithQuery := r.RequestCode.PathString() + "/" + strings.Join(r.queryParams, "/")
	r.pdu.SetPathString(tmpPathWithQuery)
	log.Debugf("SetPathString=%+v", tmpPathWithQuery)
	log.Debugf("r.pdu=%+v", r.pdu)
}

/*
 * Handle response from server for message task
 * parameter:
 *  task       the request message task
 *  response   the response message for client request
 *  env        the client environment data
 */
func (r *Request) handleResponse(task *task.MessageTask, response *libcoap.Pdu, env *task.Env) {
	isMoreBlock, eTag, block := r.env.CheckBlock(response)
	// if block is more block, sent request to server with block option
	// else display data received from server
	if isMoreBlock {
		r.pdu.MessageID = r.env.CoapSession().NewMessageID()
		r.pdu.SetOption(libcoap.OptionBlock2, uint32(block.ToInt()))
		r.pdu.SetOption(libcoap.OptionEtag, uint32(*eTag))

		// Add block2 option for waiting for response
		r.options[messages.BLOCK2] = block.ToString()
		task.SetMessage(r.pdu)
		r.env.Run(task)
	} else {
		if eTag != nil && block.NUM > 0 {
			blockKey := strconv.Itoa(*eTag) + string(response.Token)
			response = r.env.GetBlockData(blockKey)
			delete(r.env.Blocks(), blockKey)
		}
		if response.Type == libcoap.TypeNon {
			log.Debugf("Success incoming PDU(HandleResponse): %+v", response)
		}

		// Skip set analyze response data if it is the ping response
		if response.Code != 0 {
			task.AddResponse(response)
		}
	}

	// Handle Session config task and ping task after receive response message 
	// If this is response of Get session config without abnormal, restart ping task with latest parameters
	// Check if the request does not contains sid option -> if not, does not restart ping task when receive response
	// Else if this is response of Put session config with code Created -> stop the current session config task
	// Else if this is response of Delete session config with code Deleted -> stop the current session config task
	log.Debugf("r.queryParam=%v", r.queryParams)
	if (r.requestName == "session_configuration") {
		if (r.method == "GET") && (response.Code == libcoap.ResponseContent) && len(r.queryParams) > 0 {
			log.Debug("Get with sid - Client update new values to system session configuration and restart ping task.")
			RestartPingTask(response, r.env)
			RefreshSessionConfig(response, r.env, r.pdu)
		} else if (r.method == "PUT") && (response.Code == libcoap.ResponseCreated) {
			log.Debug("The new session configuration has been created. Stop the current session config task")
			RefreshSessionConfig(response, r.env, r.pdu)
		} else if (r.method == "DELETE") && (response.Code == libcoap.ResponseDeleted) {
			log.Debug("The current session configuration has been deleted. Stop the current session config task")
			RefreshSessionConfig(response, r.env, r.pdu)
		}
	}
}

/*
 * Handle request timeout for message task
 * parameter:
 *  task       the request message task
 *  env        the client environment data
 */
func handleTimeout(task *task.MessageTask, env *task.Env) {
	key := fmt.Sprintf("%x", task.GetMessage().Token)
	delete(env.Requests(), key)
	log.Info("<<< handleTimeout >>>")
}

/*
 * Handle response from server for ping message task
 * parameter:
 *  _       the request message task
 *  pdu     the the response for ping request
 */
func pingResponseHandler(_ *task.PingTask, pdu *libcoap.Pdu) {
	log.WithField("Type", pdu.Type).WithField("Code", pdu.Code).Debug("Ping Ack")
}

/*
 * Handle request timeout for ping message task
 * parameter:
 *  _       the request message task
 *  env     the client environment data
 */
func pingTimeoutHandler(_ *task.PingTask, env *task.Env) {
	log.Info("Ping Timeout #", env.GetCurrentMissingHb())

	if !env.IsHeartbeatAllowed() {
		log.Debug("Exceeded missing_hb_allowed. Stop ping task...")
		env.StopPing()

		restartConnection(env)
	}
}

/*
 * Send the request to the server.
 */
func (r *Request) Send() (res Response) {
	var config *dots_config.MessageTaskConfiguration
	if r.pdu.Type == libcoap.TypeNon {
		config = dots_config.GetSystemConfig().NonConfirmableMessageTask
	} else if r.pdu.Type == libcoap.TypeCon {
		config = dots_config.GetSystemConfig().ConfirmableMessageTask
	}
	task := task.NewMessageTask(
		r.pdu,
		time.Duration(config.TaskInterval) * time.Second,
		config.TaskRetryNumber,
		time.Duration(config.TaskTimeout) * time.Second,
		false,
		r.handleResponse,
		handleTimeout)

	r.env.Run(task)

	// Waiting for response after send a request
	pdu := r.env.WaitingForResponse(task)
	data := r.analyzeResponseData(pdu)

	if pdu == nil {
		str := "Request timeout"
		res = Response{ libcoap.ResponseInternalServerError, []byte(str) }
	} else {
		res = Response{ pdu.Code, data }
	}
	return
}

func (r *Request) analyzeResponseData(pdu *libcoap.Pdu) (data []byte) {
	var err error
	var logStr string

	if pdu == nil {
		return
	}

	log.Infof("Message Code: %v (%+v)", pdu.Code, pdu.CoapCode())
	maxAgeRes := pdu.GetOptionStringValue(libcoap.OptionMaxage)
	if maxAgeRes != "" {
		log.Infof("Max-Age Option: %v", maxAgeRes)
	}

	observe, err := pdu.GetOptionIntegerValue(libcoap.OptionObserve)
    if err != nil {
        log.WithError(err).Warn("Get observe option value failed.")
        return
	}
	if observe >= 0 {
		log.WithField("Observe Value:", observe).Info("Notification Message")
	}

	if pdu.Data == nil {
		return
	}

	log.Infof("        Raw payload: %s", pdu.Data)
	log.Infof("        Raw payload hex: \n%s", hex.Dump(pdu.Data))

	// Check if the response body data is a string message (not an object)
	if pdu.IsMessageResponse() {
		data = pdu.Data
		return
	}

	h := new(codec.CborHandle)
	dec := codec.NewDecoder(bytes.NewReader(pdu.Data), h)

	switch r.requestName {
	case "mitigation_request":
		switch r.method {
		case "GET":
			var v messages.MitigationResponse
			err = dec.Decode(&v)
			if err != nil { goto CBOR_DECODE_FAILED }
			data, err = json.Marshal(v)
			logStr = v.String()
			r.env.SetCountMitigation(v, string(pdu.Token))
			log.Debugf("Request query with token as key in map: %+v", r.env.GetAllRequestQuery())
		case "PUT":
			var v messages.MitigationResponsePut
			err = dec.Decode(&v)
			if err != nil { goto CBOR_DECODE_FAILED }
			data, err = json.Marshal(v)
			logStr = v.String()
		default:
			var v messages.MitigationRequest
			err = dec.Decode(&v)
			if err != nil { goto CBOR_DECODE_FAILED }
			data, err = json.Marshal(v)
			logStr = v.String()
		}
	case "session_configuration":
		if r.method == "GET" {
			var v messages.ConfigurationResponse
			err = dec.Decode(&v)
			if err != nil { goto CBOR_DECODE_FAILED }
			data, err = json.Marshal(v)
			logStr = v.String()
		} else {
			var v messages.SignalConfigRequest
			err = dec.Decode(&v)
			if err != nil { goto CBOR_DECODE_FAILED }
			data, err = json.Marshal(v)
			logStr = v.String()
		}
	}
	if err != nil {
		log.WithError(err).Warn("Parse object to JSON failed.")
		return
	}
	log.Infof("        CBOR decoded: %s", logStr)
	return

CBOR_DECODE_FAILED:
	log.WithError(err).Warn("CBOR Decode failed.")
	return
}

func RestartPingTask(pdu *libcoap.Pdu, env *task.Env) {
	// Check if the response body data is a string message (not an object)
	if pdu.IsMessageResponse() {
		return
	}

	h := new(codec.CborHandle)
	dec := codec.NewDecoder(bytes.NewReader(pdu.Data), h)
	var v messages.ConfigurationResponse
	err := dec.Decode(&v)
	if err != nil {
		log.WithError(err).Warn("CBOR Decode failed.")
		return
	}

	var heartbeatInterval int
	var missingHbAllowed int
	var maxRetransmit int
	var ackTimeout decimal.Decimal
	var ackRandomFactor decimal.Decimal

	if env.SessionConfigMode() == string(client_message.MITIGATING) {
		heartbeatInterval = v.SignalConfigs.MitigatingConfig.HeartbeatInterval.CurrentValue
		missingHbAllowed = v.SignalConfigs.MitigatingConfig.MissingHbAllowed.CurrentValue
		maxRetransmit = v.SignalConfigs.MitigatingConfig.MaxRetransmit.CurrentValue
		ackTimeout = v.SignalConfigs.MitigatingConfig.AckTimeout.CurrentValue.Round(2)
		ackRandomFactor = v.SignalConfigs.MitigatingConfig.AckRandomFactor.CurrentValue.Round(2)
	} else if env.SessionConfigMode() == string(client_message.IDLE) {
		heartbeatInterval = v.SignalConfigs.IdleConfig.HeartbeatInterval.CurrentValue
		missingHbAllowed = v.SignalConfigs.IdleConfig.MissingHbAllowed.CurrentValue
		maxRetransmit = v.SignalConfigs.IdleConfig.MaxRetransmit.CurrentValue
		ackTimeout = v.SignalConfigs.IdleConfig.AckTimeout.CurrentValue.Round(2)
		ackRandomFactor = v.SignalConfigs.IdleConfig.AckRandomFactor.CurrentValue.Round(2)
	}

	log.Debugf("Got session configuration data from server. Restart ping task with heatbeat-interval=%v, missing-hb-allowed=%v...", heartbeatInterval, missingHbAllowed)
	// Set max-retransmit, ack-timeout, ack-random-factor to libcoap
	env.SetRetransmitParams(maxRetransmit, ackTimeout, ackRandomFactor)
	
	env.StopPing()
	env.SetMissingHbAllowed(missingHbAllowed)
	env.Run(task.NewPingTask(
			time.Duration(heartbeatInterval) * time.Second,
			pingResponseHandler,
			pingTimeoutHandler))
}

/*
 * Refresh session config
 * 1. Stop current session config task
 * 2. Check timeFresh = 'maxAgeOption' - 'intervalBeforeMaxAge'
 *    If timeFresh > 0, Run new session config task
 *    Else, Not run new session config task
 * parameter:
 *    pdu: result response from dots_server
 *    env: env of session config
 *    message: request message
 */
func RefreshSessionConfig(pdu *libcoap.Pdu, env *task.Env, message *libcoap.Pdu) {
	env.StopSessionConfig()
	maxAgeRes, _ := strconv.Atoi(pdu.GetOptionStringValue(libcoap.OptionMaxage))
	timeFresh := maxAgeRes - env.IntervalBeforeMaxAge()
	if timeFresh > 0 {
		env.Run(task.NewSessionConfigTask(
			message,
			time.Duration(timeFresh) * time.Second,
			sessionConfigResponseHandler,
			sessionConfigTimeoutHandler))
	} else {
		log.Infof("Max-Age Option has value %+v <= %+v value of intervalBeforeMaxAge. Don't refresh session config", maxAgeRes, env.IntervalBeforeMaxAge())
	}
}

/*
 * Handle response from server for session config task
 * If Get session config is successfully
 *   1. Restart Ping task
 *   2. Refresh session config
 * parameter:
 *    t: session config task
 *    pdu: result response from server
 *    env: env session config
 */
func sessionConfigResponseHandler(t *task.SessionConfigTask, pdu *libcoap.Pdu, env *task.Env) {
	log.Infof("Message Code: %v (%+v)", pdu.Code, pdu.CoapCode())
	maxAgeRes, _ := strconv.Atoi(pdu.GetOptionStringValue(libcoap.OptionMaxage))
	log.Infof("Max-Age Option: %v", maxAgeRes)
	log.Infof("        Raw payload: %s", pdu.Data)
	log.Infof("        Raw payload hex: \n%s", hex.Dump(pdu.Data))

	// Check if the response body data is a string message (not an object)
	if pdu.IsMessageResponse() {
		return
	}

	h := new(codec.CborHandle)
	dec := codec.NewDecoder(bytes.NewReader(pdu.Data), h)
	var v messages.ConfigurationResponse
	err := dec.Decode(&v)
	if err != nil {
		log.WithError(err).Warn("CBOR Decode failed.")
		return
	}
	log.Infof("        CBOR decoded: %+v", v.String())
	if pdu.Code == libcoap.ResponseContent {
		RestartPingTask(pdu, env)
		RefreshSessionConfig(pdu, env, t.MessageTask())
	}
}

/*
 * Handle request timeout for session config task
 * Stop current session config task
 * parameter:
 *    _: session config task
 *    env: env session config
 */
func sessionConfigTimeoutHandler(_ *task.SessionConfigTask, env *task.Env) {
	log.Info("Session config refresh timeout")
	env.StopSessionConfig()
}

/*
 * Print log of notification when observe the mitigation
 * parameter:
 *  pdu   response pdu notification
 *  task  the request task for blockwise transfer process
 *  env   the client environment data
 */
func logNotification(env *task.Env, task *task.MessageTask, pdu *libcoap.Pdu) {
    log.Infof("Message Code: %v (%+v)", pdu.Code, pdu.CoapCode())

	if pdu.Data == nil {
		return
    }

    var err error
    var logStr string
    var req *libcoap.Pdu
    if task != nil {
        req = task.GetMessage()
    } else {
        req = nil
    }

    observe, err := pdu.GetOptionIntegerValue(libcoap.OptionObserve)
    if err != nil {
        log.WithError(err).Warn("Get observe option value failed.")
        return
    }
    log.WithField("Observe Value:", observe).Info("Notification Message")

	maxAgeRes := pdu.GetOptionStringValue(libcoap.OptionMaxage)
	if maxAgeRes != "" {
		log.Infof("Max-Age Option: %v", maxAgeRes)
	}

    log.Infof("        Raw payload: %s", pdu.Data)
    hex := hex.Dump(pdu.Data)
	log.Infof("        Raw payload hex: \n%s", hex)

	// Check if the response body data is a string message (not an object)
	if pdu.IsMessageResponse() {
		log.Debugf("Server send notification with error message: %+v", pdu.Data)
		return
	}

	h := new(codec.CborHandle)
    dec := codec.NewDecoder(bytes.NewReader(pdu.Data), h)

    // Identify response is mitigation or session configuration by cbor data in heximal
    if strings.Contains(hex, string(libcoap.IETF_MITIGATION_SCOPE_HEX)) {
        var v messages.MitigationResponse
        err = dec.Decode(&v)
        logStr = v.String()
        env.UpdateCountMitigation(req, v, string(pdu.Token))
        log.Debugf("Request query with token as key in map: %+v", env.GetAllRequestQuery())
    } else if strings.Contains(hex, string(libcoap.IETF_SESSION_CONFIGURATION_HEX)) {
        var v messages.ConfigurationResponse
        err = dec.Decode(&v)
        logStr = v.String()
        log.Debug("Receive session notification - Client update new values to system session configuration and restart ping task.")
		RestartPingTask(pdu, env)

		// Not refresh session config in case session config task is nil (server send notification after reset by expired Max-age)
		sessionTask := env.SessionConfigTask()
		if sessionTask != nil {
			RefreshSessionConfig(pdu, env, sessionTask.MessageTask())
		}
    } else {
        log.Warnf("Unknown notification is received.")
    }

    if err != nil {
        log.WithError(err).Warn("CBOR Decode failed.")
        return
    }
    log.Infof("        CBOR decoded: %s", logStr)
}

/*
 * Handle notification response from observer
 * If block is more block, send request with new token to retrieve remaining blocks
 * Else block is the last block, display response as server log
 * parameter:
 *  pdu   response pdu notification
 *  task  the request task for blockwise transfer process
 *  env   the client environment data
 */
func handleNotification(env *task.Env, messageTask *task.MessageTask, pdu *libcoap.Pdu) {
    isMoreBlock, eTag, block := env.CheckBlock(pdu)
    var blockKey string
    if eTag != nil {
        blockKey = strconv.Itoa(*eTag) + string(pdu.Token)
    }

    if !isMoreBlock || pdu.Type != libcoap.TypeNon {
        if eTag != nil && block.NUM > 0 {
            pdu = env.GetBlockData(blockKey)
            delete(env.Blocks(), blockKey)
        }

        log.Debugf("Success incoming PDU (NotificationResponse): %+v", pdu)
        logNotification(env, messageTask, pdu)
    } else if isMoreBlock {
        // Re-create request for block-wise transfer
        req := &libcoap.Pdu{}
        req.MessageID = env.CoapSession().NewMessageID()

        // If the messageTask is nil -> a notification from observer
        // Else -> a response from requesting to server
        if messageTask != nil {
            req = messageTask.GetMessage()
        } else {
            log.Debug("Success incoming PDU notification of first block. Re-request to retrieve remaining blocks of notification")

            req.Type = pdu.Type
            req.Code = libcoap.RequestGet

            // Create uri-path for block-wise transfer request from observation request query
            reqQuery := env.GetRequestQuery(string(pdu.Token))
            if reqQuery == nil {
                log.Error("Failed to get query param for re-request notification blocks")
                return
            }
            messageCode := messages.MITIGATION_REQUEST
            path := messageCode.PathString() + reqQuery.Query
            req.SetPathString(path)

            // Renew token value to re-request remaining blocks
            req.Token = dots_common.RandStringBytes(8)
            if eTag != nil {
                delete(env.Blocks(), blockKey)
                newBlockKey := strconv.Itoa(*eTag) + string(req.Token)
                env.Blocks()[newBlockKey] = pdu
            }
        }

        req.SetOption(libcoap.OptionBlock2, uint32(block.ToInt()))
        req.SetOption(libcoap.OptionEtag, uint32(*eTag))

        // Run new message task for re-request remaining blocks of notification
        newTask := task.NewMessageTask(
            req,
            time.Duration(2) * time.Second,
            2,
            time.Duration(10) * time.Second,
            false,
            handleResponseNotification,
            handleTimeoutNotification)

        env.Run(newTask)
    }
}

/**
 * handle notification response and check block-wise transfer
 * parameter:
 *  task       the request task in notification process (request blocks)
 *  response   the response from the request remaining blocks or the notification
 *  env        the client environment data
 */
func handleResponseNotification(task *task.MessageTask, response *libcoap.Pdu, env *task.Env){
    handleNotification(env, task, response)
}

/**
 * handle timeout in case re-request to retrieve remaining blocks of notification
 * parameter:
 *  task       the request task in notification process (request blocks)
 *  env        the client environment data
 */
func handleTimeoutNotification(task *task.MessageTask, env *task.Env) {
	key := fmt.Sprintf("%x", task.GetMessage().Token)
	delete(env.Requests(), key)
	log.Info("<<< handleTimeout Notification>>>")
}