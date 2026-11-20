//go:build windows

package rpcpipe

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	seamrpc "github.com/issueye/ada_go_v2/plugins/seam/rpc"
	"github.com/issueye/ada_go_v2/plugins/seam/seamerr"
)

type transport struct{}

func newTransport() seamrpc.Transport { return transport{} }

// fileStream 把命名管道句柄包成字节流;同步句柄不支持 deadline。
type fileStream struct{ f *os.File }

func (s *fileStream) Read(p []byte) (int, error)  { return s.f.Read(p) }
func (s *fileStream) Write(p []byte) (int, error) { return s.f.Write(p) }
func (s *fileStream) Close() error                { return s.f.Close() }

func (s *fileStream) SetDeadline(time.Time) error {
	return errors.New("rpc-pipe: 命名管道不支持 deadline")
}

func (transport) Dial(ctx context.Context, addr seamrpc.Address) (seamrpc.Stream, error) {
	select {
	case <-ctx.Done():
		return nil, seamerr.New(seamerr.ErrCanceled, "rpc-pipe", "拨号已取消", false)
	default:
	}
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(fullName(addr.Name)),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING, 0, 0,
	)
	if err != nil {
		return nil, seamrpc.Unavailable("打开命名管道失败: " + err.Error())
	}
	return &fileStream{f: os.NewFile(uintptr(h), addr.Name)}, nil
}

func (transport) Listen(ctx context.Context, addr seamrpc.Address, _ seamrpc.ListenOptions) (seamrpc.Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, seamerr.New(seamerr.ErrCanceled, "rpc-pipe", "监听已取消", false)
	}
	if addr.Name == "" || len(addr.Name) > 128 {
		return nil, seamrpc.Invalid("pipe 名字为空或过长")
	}
	sd, err := pipeSecurity()
	if err != nil {
		return nil, seamrpc.Internal("构造管道 ACL 失败")
	}
	l := &pipeListener{
		name: fullName(addr.Name), sa: sd, first: true, addr: addr, closeCh: make(chan struct{}),
	}
	// 先建一个实例,避免「监听已返回但客户端还看不到管道」的窗口。
	h, err := l.instance()
	if err != nil {
		return nil, seamrpc.Unavailable("创建管道实例失败: " + err.Error())
	}
	l.next = h
	return l, nil
}

type pipeListener struct {
	name  string
	sa    *windows.SecurityAttributes
	addr  seamrpc.Address
	first bool

	mu        sync.Mutex
	next      windows.Handle
	pending   map[windows.Handle]struct{}
	closeCh   chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
}

func (l *pipeListener) Addr() seamrpc.Address { return l.addr }

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() {
		l.closed.Store(true)
		close(l.closeCh)
		l.mu.Lock()
		if l.next != 0 {
			_ = windows.CloseHandle(l.next)
			l.next = 0
		}
		for h := range l.pending {
			_ = windows.CloseHandle(h)
			delete(l.pending, h)
		}
		l.mu.Unlock()
	})
	return nil
}

func (l *pipeListener) Accept(ctx context.Context) (seamrpc.Stream, error) {
	if l.closed.Load() {
		return nil, seamrpc.Unavailable("管道监听器已关闭")
	}
	l.mu.Lock()
	h := l.next
	l.next = 0
	l.mu.Unlock()
	if h == 0 {
		var err error
		h, err = l.instance()
		if err != nil {
			return nil, seamrpc.Unavailable("创建管道实例失败: " + err.Error())
		}
	}
	l.track(h)
	if l.closed.Load() {
		l.untrack(h)
		_ = windows.CloseHandle(h)
		return nil, seamrpc.Unavailable("管道监听器已关闭")
	}
	done := make(chan error, 1)
	go func() { done <- windows.ConnectNamedPipe(h, nil) }()
	select {
	case err := <-done:
		l.untrack(h)
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			_ = windows.CloseHandle(h)
			if l.closed.Load() {
				return nil, seamrpc.Unavailable("管道监听器已关闭")
			}
			return nil, seamrpc.Unavailable("等待客户端失败: " + err.Error())
		}
	case <-ctx.Done():
		// 自连接唤醒阻塞中的 ConnectNamedPipe,再关闭本实例。
		l.wake()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
		l.untrack(h)
		_ = windows.CloseHandle(h)
		return nil, seamerr.New(seamerr.ErrCanceled, "rpc-pipe", "accept 已取消", false)
	case <-l.closeCh:
		l.untrack(h)
		_ = windows.CloseHandle(h)
		return nil, seamrpc.Unavailable("管道监听器已关闭")
	}
	if ctx.Err() != nil || l.closed.Load() {
		l.untrack(h)
		_ = windows.CloseHandle(h)
		return nil, seamerr.New(seamerr.ErrCanceled, "rpc-pipe", "accept 已取消", false)
	}
	// 立刻补上下一个实例,让下一个客户端在 Accept 之前也能打开管道。
	l.refill()
	return &fileStream{f: os.NewFile(uintptr(h), l.name)}, nil
}

func (l *pipeListener) track(h windows.Handle) {
	l.mu.Lock()
	if l.pending == nil {
		l.pending = make(map[windows.Handle]struct{})
	}
	l.pending[h] = struct{}{}
	l.mu.Unlock()
}

func (l *pipeListener) untrack(h windows.Handle) {
	l.mu.Lock()
	delete(l.pending, h)
	l.mu.Unlock()
}

// refill 预建下一个实例;失败时留给下一次 Accept 重试。
func (l *pipeListener) refill() {
	if l.closed.Load() {
		return
	}
	h, err := l.instance()
	if err != nil {
		return
	}
	l.mu.Lock()
	if l.closed.Load() || l.next != 0 {
		l.mu.Unlock()
		_ = windows.CloseHandle(h)
		return
	}
	l.next = h
	l.mu.Unlock()
}

func (l *pipeListener) instance() (windows.Handle, error) {
	l.mu.Lock()
	first := l.first
	l.first = false
	l.mu.Unlock()
	flags := uint32(windows.PIPE_ACCESS_DUPLEX)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	mode := uint32(windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS)
	return windows.CreateNamedPipe(
		windows.StringToUTF16Ptr(l.name), flags, mode, 1, 4096, 4096, 0, l.sa,
	)
}

// wake 用一次短连接让阻塞的 ConnectNamedPipe 返回 ERROR_PIPE_CONNECTED。
func (l *pipeListener) wake() {
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(l.name),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0, nil, windows.OPEN_EXISTING, 0, 0,
	)
	if err == nil {
		_ = windows.CloseHandle(h)
	}
}

func fullName(name string) string {
	if len(name) >= 9 && name[:9] == `\\.\pipe\` {
		return name
	}
	return `\\.\pipe\` + name
}

// pipeSecurity 构造只授予当前用户与 SYSTEM 完全控制的 DACL。
func pipeSecurity() (*windows.SecurityAttributes, error) {
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sddl := "D:(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}, nil
}
