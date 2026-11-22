package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// serviceName is the Windows service of xs-hubd; the installer lets the
// crossstream group start, stop and query it without elevation.
const serviceName = "CrossStreamHub"

// pollEvery is how often a pending service state is checked.
const pollEvery = 200 * time.Millisecond

// serviceCtl starts or stops a service through the service control manager
// (args: "start"|"stop", name). Unlike sc.exe, start first waits out a
// stop still in progress: right after "stop" the old xs-hubd tears down the
// hotspot for seconds, and a start then failed or never happened.
func serviceCtl(ctx context.Context, args ...string) error {
	if len(args) != 2 {
		return fmt.Errorf("service control: want start|stop <name>, got %v", args)
	}
	op, name := args[0], args[1]
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("service %s: %w", op, err)
	}
	defer func() { _ = windows.CloseServiceHandle(m) }()
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return fmt.Errorf("service %s: %w", op, err)
	}
	s, err := windows.OpenService(m, n, windows.SERVICE_START|windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return fmt.Errorf("service %s %s: %w", op, name, err)
	}
	defer func() { _ = windows.CloseServiceHandle(s) }()
	switch op {
	case "start":
		err = startService(ctx, s)
	case "stop":
		err = stopService(s)
	default:
		err = fmt.Errorf("unknown operation")
	}
	if err != nil {
		return fmt.Errorf("service %s %s: %w", op, name, err)
	}
	return nil
}

// startService waits until a stopping service has stopped, then starts it;
// nil when it already runs or starts.
func startService(ctx context.Context, s windows.Handle) error {
	for {
		var st windows.SERVICE_STATUS
		if err := windows.QueryServiceStatus(s, &st); err != nil {
			return err
		}
		switch st.CurrentState {
		case windows.SERVICE_RUNNING, windows.SERVICE_START_PENDING:
			return nil
		case windows.SERVICE_STOPPED:
			err := windows.StartService(s, 0, nil)
			if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
				return nil
			}
			return err
		}
		select { // stop pending (or paused): wait
		case <-ctx.Done():
			return fmt.Errorf("still stopping: %w", ctx.Err())
		case <-time.After(pollEvery):
		}
	}
}

// stopService asks the service to stop and returns at once (the window
// closes); the next start waits for the stop to finish.
func stopService(s windows.Handle) error {
	var st windows.SERVICE_STATUS
	err := windows.ControlService(s, windows.SERVICE_CONTROL_STOP, &st)
	if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return nil
	}
	return err
}
