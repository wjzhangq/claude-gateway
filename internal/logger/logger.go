package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

var log = logrus.New()

// errorFileLogger writes error-level entries to daily-rotated files under logDir.
var (
	logDir       string
	fileMu       sync.Mutex
	currentDate  string
	currentFile  *os.File
	fileLogger   *logrus.Logger
)

// Init configures the global logger based on level and format strings.
func Init(level, format string) {
	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		lvl = logrus.InfoLevel
	}
	log.SetLevel(lvl)
	log.SetOutput(os.Stdout)

	if format == "json" {
		log.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	} else {
		log.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	}
}

// InitErrorLog sets up daily-rotated error log files in dir.
// If dir is empty, file logging is disabled.
func InitErrorLog(dir string) {
	if dir == "" {
		return
	}
	logDir = dir
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Errorf("failed to create log dir %s: %v", logDir, err)
		return
	}
	fileLogger = logrus.New()
	fileLogger.SetLevel(logrus.DebugLevel)
	fileLogger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
	})
	// Open the initial file
	fileMu.Lock()
	rotateIfNeeded()
	fileMu.Unlock()
	log.Infof("error log dir initialized: %s", logDir)
}

// rotateIfNeeded switches to a new daily log file if the date has changed.
// Must be called with fileMu held or during init.
func rotateIfNeeded() {
	today := time.Now().Format("2006-01-02")
	if today == currentDate && currentFile != nil {
		return
	}
	// Close old file
	if currentFile != nil {
		currentFile.Close()
	}
	filename := filepath.Join(logDir, fmt.Sprintf("error-%s.log", today))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Errorf("failed to open error log file %s: %v", filename, err)
		currentFile = nil
		return
	}
	currentFile = f
	currentDate = today
	if fileLogger != nil {
		fileLogger.SetOutput(f)
	}
}

// LogErrorRequest writes a structured error entry (with request details) to the
// daily error log file. It also logs a summary to stdout at error level.
// fileOnlyKeys lists field keys that should only appear in the file log (e.g.
// "request_body") to avoid flooding the console with large payloads.
func LogErrorRequest(msg string, fields logrus.Fields, fileOnlyKeys ...string) {
	// Build console fields: exclude file-only keys
	consoleFields := make(logrus.Fields, len(fields))
	for k, v := range fields {
		consoleFields[k] = v
	}
	for _, k := range fileOnlyKeys {
		delete(consoleFields, k)
	}
	log.WithFields(consoleFields).Error(msg)

	// Write full fields (including body) to file if configured
	if fileLogger == nil || logDir == "" {
		return
	}
	fileMu.Lock()
	rotateIfNeeded()
	fileLogger.WithFields(fields).Error(msg)
	fileMu.Unlock()
}

// Backend log: separate daily-rotated files for backend anomalies (errors + zero-token).
// dasheng-prefixed backends are written to dasheng-YYYY-MM-DD.log; others to backend-YYYY-MM-DD.log.
var (
	backendLogDir string

	backendMu     sync.Mutex
	backendDate   string
	backendFile   *os.File
	backendLogger *logrus.Logger

	dashengMu     sync.Mutex
	dashengDate   string
	dashengFile   *os.File
	dashengLogger *logrus.Logger
)

func newFileLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	l.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
	})
	return l
}

// InitBackendLog sets up daily-rotated backend log files in dir.
// If dir is empty, backend file logging is disabled.
func InitBackendLog(dir string) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Errorf("failed to create log dir %s: %v", dir, err)
		return
	}
	backendLogDir = dir
	backendLogger = newFileLogger()
	dashengLogger = newFileLogger()
	backendMu.Lock()
	rotateBackendIfNeeded()
	backendMu.Unlock()
	dashengMu.Lock()
	rotateDashengIfNeeded()
	dashengMu.Unlock()
	log.Infof("backend log dir initialized: %s", dir)
}

func rotateBackendIfNeeded() {
	today := time.Now().Format("2006-01-02")
	if today == backendDate && backendFile != nil {
		return
	}
	if backendFile != nil {
		backendFile.Close()
	}
	filename := filepath.Join(backendLogDir, fmt.Sprintf("backend-%s.log", today))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Errorf("failed to open backend log file %s: %v", filename, err)
		backendFile = nil
		return
	}
	backendFile = f
	backendDate = today
	backendLogger.SetOutput(f)
}

func rotateDashengIfNeeded() {
	today := time.Now().Format("2006-01-02")
	if today == dashengDate && dashengFile != nil {
		return
	}
	if dashengFile != nil {
		dashengFile.Close()
	}
	filename := filepath.Join(backendLogDir, fmt.Sprintf("dasheng-%s.log", today))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Errorf("failed to open dasheng log file %s: %v", filename, err)
		dashengFile = nil
		return
	}
	dashengFile = f
	dashengDate = today
	dashengLogger.SetOutput(f)
}

// LogBackendRequest writes a structured backend anomaly entry to the appropriate
// daily log file. dasheng-prefixed backends go to dasheng-YYYY-MM-DD.log; all
// others go to backend-YYYY-MM-DD.log. A summary is also logged to stdout.
func LogBackendRequest(msg string, fields logrus.Fields, fileOnlyKeys ...string) {
	consoleFields := make(logrus.Fields, len(fields))
	for k, v := range fields {
		consoleFields[k] = v
	}
	for _, k := range fileOnlyKeys {
		delete(consoleFields, k)
	}
	log.WithFields(consoleFields).Warn(msg)

	if backendLogDir == "" {
		return
	}

	backendName, _ := fields["backend"].(string)
	isDasheng := len(backendName) >= 7 && backendName[:7] == "dasheng"

	if isDasheng {
		dashengMu.Lock()
		rotateDashengIfNeeded()
		if dashengFile != nil {
			dashengLogger.WithFields(fields).Warn(msg)
		}
		dashengMu.Unlock()
	} else {
		backendMu.Lock()
		rotateBackendIfNeeded()
		if backendFile != nil {
			backendLogger.WithFields(fields).Warn(msg)
		}
		backendMu.Unlock()
	}
}

// Get returns the configured logrus logger.
func Get() *logrus.Logger { return log }

// WithField delegates to the global logger.
func WithField(key string, value interface{}) *logrus.Entry {
	return log.WithField(key, value)
}

// WithFields delegates to the global logger.
func WithFields(fields logrus.Fields) *logrus.Entry {
	return log.WithFields(fields)
}

func Info(args ...interface{})                 { log.Info(args...) }
func Infof(f string, args ...interface{})      { log.Infof(f, args...) }
func Warn(args ...interface{})                 { log.Warn(args...) }
func Warnf(f string, args ...interface{})      { log.Warnf(f, args...) }
func Error(args ...interface{})                { log.Error(args...) }
func Errorf(f string, args ...interface{})     { log.Errorf(f, args...) }
func Debug(args ...interface{})                { log.Debug(args...) }
func Debugf(f string, args ...interface{})     { log.Debugf(f, args...) }
func Fatal(args ...interface{})                { log.Fatal(args...) }
func Fatalf(f string, args ...interface{})     { log.Fatalf(f, args...) }
