package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"
)

// connTimeout bounds one control-socket exchange.
const connTimeout = 10 * time.Second

// Serve answers control-socket requests on ln until ctx is done, then closes ln.
func Serve(ctx context.Context, ln net.Listener, svc *Service, log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("control socket accept: %w", err)
		}
		go handle(ctx, c, svc, log)
	}
}

func handle(ctx context.Context, c net.Conn, svc *Service, log *slog.Logger) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(connTimeout))
	var req Request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		log.Warn("control request", "err", err)
		return
	}
	resp := dispatch(WithActor(ctx, "xs-ctl"), svc, req)
	if resp.Error != "" {
		log.Info("control op failed", "op", req.Op, "err", resp.Error)
	}
	if err := json.NewEncoder(c).Encode(resp); err != nil {
		log.Warn("control response", "err", err)
	}
}

// CtlAuditLimit is the most audit entries one request returns.
const CtlAuditLimit = AuditKeep

func dispatch(ctx context.Context, svc *Service, req Request) Response {
	var resp Response
	var err error
	switch req.Op {
	case OpEnroll:
		var r EnrollResult
		if r, err = svc.Enroll(ctx, req.Name); err == nil {
			resp.Enroll = &r
		}
	case OpHubs:
		resp.Hubs, err = svc.Hubs(ctx)
	case OpRevoke:
		err = svc.Revoke(ctx, req.Name)
	case OpRename:
		err = svc.Rename(ctx, req.Name, req.NewName)
	case OpSessions:
		resp.Sessions, err = svc.Sessions(ctx)
	case OpForward:
		err = svc.ForwardAllow(ctx, req.Name, req.N)
	case OpInfo:
		var in ServerInfo
		if in, err = svc.Info(ctx); err == nil {
			resp.Info = &in
		}
	case OpTunnelPort:
		err = svc.SetTunnelPort(ctx, req.N)
	case OpRestart:
		err = svc.Restart(ctx)
	case OpAudit:
		resp.Audit = svc.AuditLog(ctx, min(max(req.N, 0), CtlAuditLimit))
	case OpUserAdd:
		err = svc.AddUser(ctx, req.Name, req.Password)
	case OpUserPasswd:
		err = svc.SetPassword(ctx, req.Name, req.Password)
	case OpUserDel:
		err = svc.DelUser(ctx, req.Name)
	case OpUserList:
		resp.Users = svc.Users(ctx)
	case OpUserTOTP:
		resp.TOTPURI, err = svc.SetTOTP(ctx, req.Name, !req.Off)
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}

// Client calls a Service over the control socket at Socket.
type Client struct {
	Socket string
}

// Enroll calls Service.Enroll.
func (c Client) Enroll(ctx context.Context, name string) (EnrollResult, error) {
	resp, err := c.call(ctx, Request{Op: OpEnroll, Name: name})
	if err != nil {
		return EnrollResult{}, err
	}
	if resp.Enroll == nil {
		return EnrollResult{}, errors.New("enroll: empty response")
	}
	return *resp.Enroll, nil
}

// Hubs calls Service.Hubs.
func (c Client) Hubs(ctx context.Context) ([]Hub, error) {
	resp, err := c.call(ctx, Request{Op: OpHubs})
	return resp.Hubs, err
}

// Revoke calls Service.Revoke.
func (c Client) Revoke(ctx context.Context, name string) error {
	_, err := c.call(ctx, Request{Op: OpRevoke, Name: name})
	return err
}

// ForwardAllow calls Service.ForwardAllow.
func (c Client) ForwardAllow(ctx context.Context, name string, n int) error {
	_, err := c.call(ctx, Request{Op: OpForward, Name: name, N: n})
	return err
}

// Sessions calls Service.Sessions.
func (c Client) Sessions(ctx context.Context) ([]Session, error) {
	resp, err := c.call(ctx, Request{Op: OpSessions})
	return resp.Sessions, err
}

// Rename calls Service.Rename.
func (c Client) Rename(ctx context.Context, name, newName string) error {
	_, err := c.call(ctx, Request{Op: OpRename, Name: name, NewName: newName})
	return err
}

// SetTunnelPort calls Service.SetTunnelPort.
func (c Client) SetTunnelPort(ctx context.Context, port int) error {
	_, err := c.call(ctx, Request{Op: OpTunnelPort, N: port})
	return err
}

// Restart calls Service.Restart.
func (c Client) Restart(ctx context.Context) error {
	_, err := c.call(ctx, Request{Op: OpRestart})
	return err
}

// Info calls Service.Info.
func (c Client) Info(ctx context.Context) (ServerInfo, error) {
	resp, err := c.call(ctx, Request{Op: OpInfo})
	if err != nil {
		return ServerInfo{}, err
	}
	if resp.Info == nil {
		return ServerInfo{}, errors.New("info: empty response")
	}
	return *resp.Info, nil
}

// AuditLog returns up to n newest audit entries (0 = all kept).
func (c Client) AuditLog(ctx context.Context, n int) ([]AuditEntry, error) {
	resp, err := c.call(ctx, Request{Op: OpAudit, N: n})
	return resp.Audit, err
}

// AddUser calls Service.AddUser.
func (c Client) AddUser(ctx context.Context, name, password string) error {
	_, err := c.call(ctx, Request{Op: OpUserAdd, Name: name, Password: password})
	return err
}

// SetPassword calls Service.SetPassword.
func (c Client) SetPassword(ctx context.Context, name, password string) error {
	_, err := c.call(ctx, Request{Op: OpUserPasswd, Name: name, Password: password})
	return err
}

// DelUser calls Service.DelUser.
func (c Client) DelUser(ctx context.Context, name string) error {
	_, err := c.call(ctx, Request{Op: OpUserDel, Name: name})
	return err
}

// Users calls Service.Users.
func (c Client) Users(ctx context.Context) ([]UserInfo, error) {
	resp, err := c.call(ctx, Request{Op: OpUserList})
	return resp.Users, err
}

// SetTOTP calls Service.SetTOTP.
func (c Client) SetTOTP(ctx context.Context, name string, on bool) (string, error) {
	resp, err := c.call(ctx, Request{Op: OpUserTOTP, Name: name, Off: !on})
	return resp.TOTPURI, err
}

func (c Client) call(ctx context.Context, req Request) (Response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return Response{}, fmt.Errorf("connect to xs-server at %s: %w", c.Socket, err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(connTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, fmt.Errorf("%s request: %w", req.Op, err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("%s response: %w", req.Op, err)
	}
	if resp.Error != "" {
		return resp, fmt.Errorf("%s: %s", req.Op, resp.Error)
	}
	return resp, nil
}
