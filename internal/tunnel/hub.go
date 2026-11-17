package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

var errClosed = errors.New("tunnel closed")

type pendingEvent struct {
	kind  string
	frame Frame
	data  []byte
	err   error
}

type pending struct {
	events chan pendingEvent
	mint   chan Frame
	closed chan struct{}
	once   sync.Once
}

func newPending() *pending {
	return &pending{
		events: make(chan pendingEvent, 256),
		mint:   make(chan Frame, 1),
		closed: make(chan struct{}),
	}
}

func (p *pending) push(ev pendingEvent) {
	select {
	case p.events <- ev:
	case <-p.closed:
	}
}

func (p *pending) close() {
	p.once.Do(func() { close(p.closed) })
}

type Hub struct {
	mu         sync.RWMutex
	conns      map[string]*Conn
	onNotify   func(deviceID, tenantID string, f Frame)
	onRegister func(deviceID, tenantID string, f Frame)
}

func NewHub(onNotify, onRegister func(deviceID, tenantID string, f Frame)) *Hub {
	return &Hub{
		conns:      make(map[string]*Conn),
		onNotify:   onNotify,
		onRegister: onRegister,
	}
}

func (h *Hub) add(c *Conn) {
	h.mu.Lock()
	old := h.conns[c.DeviceID]
	h.conns[c.DeviceID] = c
	h.mu.Unlock()
	if old != nil && old != c {
		old.Close()
	}
}

func (h *Hub) remove(c *Conn) {
	h.mu.Lock()
	if h.conns[c.DeviceID] == c {
		delete(h.conns, c.DeviceID)
	}
	h.mu.Unlock()
}

func (h *Hub) Get(deviceID string) *Conn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.conns[deviceID]
}

func (h *Hub) Online(deviceID string) bool {
	return h.Get(deviceID) != nil
}

func (h *Hub) Kick(deviceID string) {
	if c := h.Get(deviceID); c != nil {
		c.Close()
	}
}

func (h *Hub) KickTenant(tenantID string) {
	h.mu.RLock()
	var list []*Conn
	for _, c := range h.conns {
		if c.TenantID == tenantID {
			list = append(list, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range list {
		c.Close()
	}
}

func (h *Hub) CloseAll() {
	h.mu.RLock()
	list := make([]*Conn, 0, len(h.conns))
	for _, c := range h.conns {
		list = append(list, c)
	}
	h.mu.RUnlock()
	for _, c := range list {
		c.Close()
	}
}

type Conn struct {
	ws       *websocket.Conn
	DeviceID string
	TenantID string
	Device   string
	Tenant   string
	Version  string
	hub      *Hub

	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[uint64]*pending
	nextID    atomic.Uint64
	closed    chan struct{}
	closeOnce sync.Once
}

func NewConn(ws *websocket.Conn, deviceID, tenantID, device, tenant, version string, hub *Hub) *Conn {
	return &Conn{
		ws:       ws,
		DeviceID: deviceID,
		TenantID: tenantID,
		Device:   device,
		Tenant:   tenant,
		Version:  version,
		hub:      hub,
		pending:  make(map[uint64]*pending),
		closed:   make(chan struct{}),
	}
}

func (c *Conn) Serve(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.hub.add(c)
	defer c.hub.remove(c)
	defer c.shutdown()
	go c.ping(ctx)

	if err := c.Send(ctx, Frame{T: "hello", Device: c.Device, Tenant: c.Tenant, Version: c.Version}); err != nil {
		return
	}
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		c.dispatch(f)
	}
}

func (c *Conn) ping(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.Close()
				return
			}
		}
	}
}

func (c *Conn) Close() {
	c.shutdown()
}

func (c *Conn) shutdown() {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.ws.Close(websocket.StatusNormalClosure, "closed")
		c.pendingMu.Lock()
		for id, p := range c.pending {
			p.close()
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()
	})
}

func (c *Conn) Send(ctx context.Context, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return errClosed
	default:
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.ws.Write(wctx, websocket.MessageText, b)
}

func (c *Conn) dispatch(f Frame) {
	switch f.T {
	case "res.start":
		c.push(f.ID, pendingEvent{kind: "start", frame: f})
	case "res.chunk":
		c.push(f.ID, pendingEvent{kind: "chunk", data: f.Body})
	case "res.end":
		c.push(f.ID, pendingEvent{kind: "end"})
	case "res.error":
		c.push(f.ID, pendingEvent{kind: "end", err: errors.New(f.Error)})
	case "pair.minted":
		if p := c.lookup(f.ID); p != nil {
			select {
			case p.mint <- f:
			default:
			}
		}
	case "pair.register":
		if c.hub.onRegister != nil {
			c.hub.onRegister(c.DeviceID, c.TenantID, f)
		}
	case "notify":
		if c.hub.onNotify != nil {
			c.hub.onNotify(c.DeviceID, c.TenantID, f)
		}
	case "ping":
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.Send(ctx, Frame{T: "pong"})
		}()
	case "pong", "hello.ack":
	}
}

func (c *Conn) push(id uint64, ev pendingEvent) {
	if p := c.lookup(id); p != nil {
		p.push(ev)
	}
}

func (c *Conn) lookup(id uint64) *pending {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	return c.pending[id]
}

func (c *Conn) register(id uint64, p *pending) {
	c.pendingMu.Lock()
	c.pending[id] = p
	c.pendingMu.Unlock()
}

func (c *Conn) release(id uint64) {
	c.pendingMu.Lock()
	p := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if p != nil {
		p.close()
	}
}

func (c *Conn) Do(ctx context.Context, req Frame, onStart func(Frame) error, onChunk func([]byte) error) error {
	id := c.nextID.Add(1)
	req.ID = id
	req.T = "req"
	p := newPending()
	c.register(id, p)
	defer c.release(id)

	if err := c.Send(ctx, req); err != nil {
		return err
	}
	abort := make(chan struct{})
	defer close(abort)
	go func() {
		select {
		case <-ctx.Done():
			c.sendAbort(id)
		case <-abort:
		}
	}()

	started := false
	for {
		select {
		case ev := <-p.events:
			switch ev.kind {
			case "start":
				if err := onStart(ev.frame); err != nil {
					c.sendAbort(id)
					return err
				}
				started = true
			case "chunk":
				if !started {
					return errors.New("response chunk before start")
				}
				if err := onChunk(ev.data); err != nil {
					c.sendAbort(id)
					return err
				}
			case "end":
				if ev.err != nil {
					return ev.err
				}
				if !started {
					return errors.New("response ended before start")
				}
				return nil
			}
		case <-ctx.Done():
			c.sendAbort(id)
			return ctx.Err()
		case <-c.closed:
			return errClosed
		}
	}
}

func (c *Conn) Mint(ctx context.Context) (Frame, error) {
	id := c.nextID.Add(1)
	p := newPending()
	c.register(id, p)
	defer c.release(id)
	if err := c.Send(ctx, Frame{T: "pair.mint", ID: id}); err != nil {
		return Frame{}, err
	}
	select {
	case f := <-p.mint:
		return f, nil
	case ev := <-p.events:
		if ev.err != nil {
			return Frame{}, ev.err
		}
		return Frame{}, fmt.Errorf("pair mint returned no code")
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-c.closed:
		return Frame{}, errClosed
	}
}

func (c *Conn) RegisterPair(f Frame) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.T = "pair.register"
	return c.Send(ctx, f)
}

func (c *Conn) Notify(f Frame) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f.T = "notify"
	return c.Send(ctx, f)
}

func (c *Conn) sendAbort(id uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Send(ctx, Frame{T: "abort", ID: id})
}
