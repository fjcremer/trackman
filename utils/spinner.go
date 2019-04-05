package utils

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/spf13/viper"
)

// Options holds running options for a Spinner
type Options struct {
	Sink                *SpinnerSink
	NotificationManager *NotificationManager
}

// Spinner is the main component that runs a process
type Spinner struct {
	uuid    string
	options *Options
	cmd     string
	args    []string
}

// NewSpinner creates a new instance of Spinner based on the Options
func NewSpinner(ctx context.Context, command string, options *Options) (*Spinner, error) {
	if options.Sink == nil {
		panic("no sink")
	}

	if options.NotificationManager == nil {
		panic("no notification manager")
	}

	parts := strings.Split(command, " ")
	if len(parts) < 1 {
		return nil, errors.New("bad command")
	}

	return &Spinner{
		uuid:    uuid.New().String(),
		options: options,
		cmd:     parts[0],
		args:    parts[1:],
	}, nil
}

// Run runs the process required
func (s *Spinner) Run(ctx context.Context) error {
	s.push(ctx, NewEvent(s, EventRunRequested, nil))

	ctx, cancel := context.WithTimeout(ctx, viper.GetDuration("timeout"))
	defer cancel()

	cmd := exec.CommandContext(ctx, s.cmd, s.args...)
	cmd.Stderr = s.options.Sink.StdErr
	cmd.Stdout = s.options.Sink.StdOut
	err := cmd.Start()
	if err != nil {
		s.push(ctx, NewEvent(s, EventRunError, nil))

		return err
	}

	s.push(ctx, NewEvent(s, EventRunStarted, nil))

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// The program has exited with an exit code != 0
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				s.push(ctx, NewEvent(s, EventRunFail, status))
			}
		} else {
			// wait error
			s.push(ctx, NewEvent(s, EventRunWaitError, s))

			return exitErr
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		s.push(ctx, NewEvent(s, EventRunTimeout, nil))

		return ctx.Err()
	}

	s.push(ctx, NewEvent(s, EventRunSuccess, nil))

	return nil
}

func (s *Spinner) push(ctx context.Context, event *Event) {
	s.options.NotificationManager.Push(ctx, event)
}