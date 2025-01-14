//go:build linux
// +build linux

package fastudp

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/constructorvirgil/fastudp/netpoll"
	"github.com/constructorvirgil/fastudp/netudp"
)

type Server struct {
	wg      sync.WaitGroup
	handler EventHandler
	loops   map[int]*eventLoop
	wp      chan []byte
	pool    sync.Pool
	closed  atomic.Value
	sync.Mutex
}

func NewUDPServer(network, addr string, reusePort bool, listenerN int, mtu int, handler EventHandler) (*Server, error) {
	if !netudp.IsUDP(network) {
		return nil, fmt.Errorf("unknown network: %v", network)
	}

	svr := &Server{
		handler: handler,
		loops:   make(map[int]*eventLoop),
		wp:      make(chan []byte, WriteEventSize),
	}

	svr.pool.New = func() interface{} {
		return make([]byte, mtu)
	}

	if reusePort {
		if listenerN <= 0 {
			listenerN = runtime.NumCPU()
		}
	} else {
		listenerN = 1
	}

	if err := svr.start(network, addr, reusePort, listenerN, mtu); err != nil {
		return nil, err
	}

	svr.closed.Store(false)
	return svr, nil
}

func (s *Server) start(network, addr string, reusePort bool, listenerN, mtu int) error {
	for i := 0; i < listenerN; i++ {
		l, err := listen(network, addr, reusePort)
		if err != nil {
			return err
		}

		poller, err := netpoll.PollerInit()
		if err != nil {
			return err
		}

		loop := newEventLoop(s, l, poller, mtu)
		s.loops[loop.l.fd] = loop
		poller.Add(loop.l.fd, "r")

		go loop.run()
		go loop.readLoop()

		s.wg.Add(1)
	}

	return nil
}

func (svr *Server) Shutdown() {
	if svr.closed.Load().(bool) {
		return
	}

	loops := make(map[int]*eventLoop)
	svr.Lock()
	for fd, loop := range svr.loops {
		loops[fd] = loop
	}
	svr.Unlock()

	for _, loop := range loops {
		if !loop.closed.Load().(bool) {
			loop.Close(nil)
		}
	}

	svr.wg.Wait()
	svr.closed.Store(true)
}

func (svr *Server) eventLoopClosed(loop *eventLoop, err error) {
	svr.Lock()
	defer svr.Unlock()

	delete(svr.loops, loop.l.fd)
	svr.wg.Done()

	if err != nil {
		svr.handler.OnError(err)
	}
}

// TODO: need load balancing?
func (svr *Server) WriteTo(addr *net.UDPAddr, data []byte) (int, error) {
	if svr.closed.Load().(bool) {
		return 0, fmt.Errorf("server closed")
	}

	var loop *eventLoop
	svr.Lock()
	for _, l := range svr.loops {
		loop = l
		break
	}
	svr.Unlock()

	return loop.writeTo(data, addr)
}
