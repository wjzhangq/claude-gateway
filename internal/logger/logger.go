package logger

import (
	"os"

	"github.com/sirupsen/logrus"
)

var log = logrus.New()

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
