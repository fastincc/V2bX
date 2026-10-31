package hy2

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type serverLogger struct {
	Auth   *V2bX
	Tag    string
	logger *zap.Logger
}

var logLevelMap = map[string]zapcore.Level{
	"debug": zapcore.DebugLevel,
	"info":  zapcore.InfoLevel,
	"warn":  zapcore.WarnLevel,
	"error": zapcore.ErrorLevel,
}

var logFormatMap = map[string]zapcore.EncoderConfig{
	"console": {
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     zapcore.RFC3339TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	},
	"json": {
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.EpochMillisTimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
	},
}

func (l *serverLogger) Connect(addr net.Addr, uuid string, tx uint64) {
	l.logger.Info("client connected", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.Uint64("tx", tx))
}

func (l *serverLogger) Disconnect(addr net.Addr, uuid string, err error) {
	if l.Auth != nil {
		defer l.Auth.disconnect(uuid)
	}
	l.logger.Info("client disconnected", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.String("error", safeLogError(err)))
}

func (l *serverLogger) TCPRequest(addr net.Addr, uuid, reqAddr string) {
	l.checkAdmission(addr, uuid)
	l.logger.Debug("TCP request", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.String("reqAddr", redactIdentity(reqAddr, uuid)))
}

func (l *serverLogger) TCPError(addr net.Addr, uuid, reqAddr string, err error) {
	if err == nil {
		l.logger.Debug("TCP closed", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.String("reqAddr", redactIdentity(reqAddr, uuid)))
	} else {
		l.logger.Debug("TCP error", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.String("reqAddr", redactIdentity(reqAddr, uuid)), zap.String("error", safeLogError(err)))
	}
}

func (l *serverLogger) UDPRequest(addr net.Addr, uuid string, sessionId uint32, reqAddr string) {
	l.checkAdmission(addr, uuid)
	l.logger.Debug("UDP request", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.Uint32("sessionId", sessionId), zap.String("reqAddr", redactIdentity(reqAddr, uuid)))
}

func (l *serverLogger) UDPError(addr net.Addr, uuid string, sessionId uint32, err error) {
	if err == nil {
		l.logger.Debug("UDP closed", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.Uint32("sessionId", sessionId))
	} else {
		l.logger.Debug("UDP error", zap.String("addr", addr.String()), zap.String("user_id", l.safeUserID(uuid)), zap.Uint32("sessionId", sessionId), zap.String("error", safeLogError(err)))
	}
}

func initLogger(logLevel string, logFormat string) (*zap.Logger, error) {
	level, ok := logLevelMap[strings.ToLower(logLevel)]
	if !ok {
		return nil, fmt.Errorf("unsupported log level: %s", logLevel)
	}
	enc, ok := logFormatMap[strings.ToLower(logFormat)]
	if !ok {
		return nil, fmt.Errorf("unsupported log format: %s", logFormat)
	}
	c := zap.Config{
		Level:             zap.NewAtomicLevelAt(level),
		DisableCaller:     true,
		DisableStacktrace: true,
		Encoding:          strings.ToLower(logFormat),
		EncoderConfig:     enc,
		OutputPaths:       []string{"stderr"},
		ErrorOutputPaths:  []string{"stderr"},
	}
	logger, err := c.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize logger: %s", err)
	}
	return logger, nil
}

func extractIPFromAddr(addr net.Addr) string {
	switch v := addr.(type) {
	case *net.TCPAddr:
		return v.IP.String()
	case *net.UDPAddr:
		return v.IP.String()
	case *net.IPAddr:
		return v.IP.String()
	default:
		return ""
	}
}

func (l *serverLogger) safeUserID(id string) string {
	if l.Auth == nil {
		return "unmapped"
	}
	l.Auth.mutex.RLock()
	defer l.Auth.mutex.RUnlock()
	generation, _, _ := strings.Cut(id, "/connection-")
	uid := l.Auth.uidLocked(generation)
	if session := l.Auth.sessions[id]; session != nil {
		uid = session.uid
	}
	if uid == 0 {
		uid = l.Auth.usersMap[id]
	}
	if uid > 0 {
		return strconv.Itoa(uid)
	}
	return "unmapped"
}
func safeLogError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return "closed"
	}
	var n net.Error
	if errors.As(err, &n) && n.Timeout() {
		return "timeout"
	}
	return "transport error"
}
func redactIdentity(value, id string) string {
	if id == "" {
		return value
	}
	return strings.ReplaceAll(value, id, "[redacted]")
}

func (l *serverLogger) checkAdmission(addr net.Addr, id string) {
	if l.Auth == nil {
		return
	}
	l.Auth.mutex.Lock()
	defer l.Auth.mutex.Unlock()
	session := l.Auth.sessions[id]
	if session == nil || session.denied {
		return
	}
	identity, ok := l.Auth.identities[session.generation]
	if !ok || identity.retired || l.Auth.active[identity.credential] != session.generation {
		session.denied = true
		return
	}
	limit, err := limiter.GetLimiter(l.Tag)
	if err != nil {
		session.denied = true
		return
	}
	_, reject := limit.CheckLimit(format.UserTag(l.Tag, identity.credential), extractIPFromAddr(addr), addr.Network() == "tcp", true)
	session.denied = reject
}
