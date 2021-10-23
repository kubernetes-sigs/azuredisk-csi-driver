/*
Copyright 2021 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package batch

import (
	"io"
	"log"
)

type standardLogger struct {
	logger  *log.Logger
	verbose bool
}

type StandardLoggerOption func(l *standardLogger)

type LoggingFunc func(format string, v ...interface{})

type loggerAdapter struct {
	verboseFn LoggingFunc
	infoFn    LoggingFunc
	warningFn LoggingFunc
	errorFn   LoggingFunc
}

type LoggerAdapterOption func(l *loggerAdapter)

var _ Logger = &standardLogger{}

// Logs to the verbose log. It handles the arguments in the manner of fmt.Printf.
func (l *standardLogger) Verbosef(format string, v ...interface{}) {
	if l.verbose {
		l.logger.Printf("VERBOSE: "+format, v...)
	}
}

// Logs to the informational log. It handles the arguments in the manner of fmt.Printf.
func (l *standardLogger) Infof(format string, v ...interface{}) {
	l.logger.Printf("INFO: "+format, v...)
}

// Logs to the warning log. It handles the arguments in the manner of fmt.Printf.
func (l *standardLogger) Warningf(format string, v ...interface{}) {
	l.logger.Printf("WARN: "+format, v...)
}

// Logs to the error log. It handles the arguments in the manner of fmt.Printf.
func (l *standardLogger) Errorf(format string, v ...interface{}) {
	l.logger.Printf("ERROR: "+format, v...)
}

/// applyDefaults applies default options to the standardLogger.
func (l *standardLogger) applyDefaults() {
	if l.logger == nil {
		l.logger = log.Default()
	}
}

// NewStandardLogger creates a new default logger.
func NewStandardLogger(options ...StandardLoggerOption) Logger {
	logger := &standardLogger{}

	for _, option := range options {
		option(logger)
	}

	logger.applyDefaults()

	return logger
}

// WithOutput sets the destination to which log data will be written. The prefix appears
// at the beginning of each generated log line, or after the log header if the Lmsgprefix
// flag is provided. The flag argument defines the logging properties.
func WithOutput(output io.Writer, prefix string, flag int) StandardLoggerOption {
	return func(l *standardLogger) {
		l.logger = log.New(output, prefix, flag)
	}
}

// WithVerboseLogging enables verbose logging on the standard logger.
func WithVerboseLogging() StandardLoggerOption {
	return func(l *standardLogger) {
		l.verbose = true
	}
}

// Logs to the verbose log. It handles the arguments in the manner of fmt.Printf.
func (l *loggerAdapter) Verbosef(format string, v ...interface{}) {
	l.verboseFn(format, v...)
}

// Logs to the informational log. It handles the arguments in the manner of fmt.Printf.
func (l *loggerAdapter) Infof(format string, v ...interface{}) {
	l.infoFn(format, v...)
}

// Logs to the warning log. It handles the arguments in the manner of fmt.Printf.
func (l *loggerAdapter) Warningf(format string, v ...interface{}) {
	l.warningFn(format, v...)
}

// Logs to the error log. It handles the arguments in the manner of fmt.Printf.
func (l *loggerAdapter) Errorf(format string, v ...interface{}) {
	l.errorFn(format, v...)
}

/// applyDefaults applies default options to the loggerAdapter.
func (l *loggerAdapter) applyDefaults() {
	nullLoggerFn := func(format string, v ...interface{}) {}

	if l.verboseFn == nil {
		l.verboseFn = nullLoggerFn
	}
	if l.infoFn == nil {
		l.infoFn = nullLoggerFn
	}
	if l.warningFn == nil {
		l.warningFn = nullLoggerFn
	}
	if l.errorFn == nil {
		l.errorFn = nullLoggerFn
	}
}

// NewLoggerAdapter returns a new logger that adapts an existing logger to the Logger interface.
func NewLoggerAdapter(options ...LoggerAdapterOption) Logger {
	logger := &loggerAdapter{}

	for _, option := range options {
		option(logger)
	}

	logger.applyDefaults()

	return logger
}

// WithVerboseLogger applies the verbose logging function to a logger adapter.
func WithVerboseLogger(verboseFn LoggingFunc) LoggerAdapterOption {
	return func(l *loggerAdapter) {
		l.verboseFn = verboseFn
	}
}

// WithInfoLogger applies the informational logging function to a logger adapter.
func WithInfoLogger(infoFn LoggingFunc) LoggerAdapterOption {
	return func(l *loggerAdapter) {
		l.infoFn = infoFn
	}
}

// WithWarningLogger applies the warning logging function to a logger adapter.
func WithWarningLogger(warningFn LoggingFunc) LoggerAdapterOption {
	return func(l *loggerAdapter) {
		l.warningFn = warningFn
	}
}

// WithVerboseLogger applies the verbose logging function to a logger adapter.
func WithErrorLogger(errorFn LoggingFunc) LoggerAdapterOption {
	return func(l *loggerAdapter) {
		l.errorFn = errorFn
	}
}
