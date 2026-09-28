package repository

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// A real TCP proxy completes Redis initialization, then discards server replies.
// The client is blocked in socket Read, not in a fake context-aware operation.
func redisReplyBlackhole(t *testing.T) (string, *atomic.Bool) {
	t.Helper()
	upstream := miniredis.RunT(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	blocked := &atomic.Bool{}
	var mu sync.Mutex
	var connections []net.Conn
	var workers sync.WaitGroup
	acceptedDone := make(chan struct{})
	go func() {
		defer close(acceptedDone)
		for {
			down, err := listener.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", upstream.Addr())
			if err != nil {
				_ = down.Close()
				continue
			}
			mu.Lock()
			connections = append(connections, down, up)
			mu.Unlock()
			workers.Add(2)
			go func() { defer workers.Done(); _, _ = io.Copy(up, down) }()
			go func() {
				defer workers.Done()
				buf := make([]byte, 4096)
				for {
					n, err := up.Read(buf)
					if n > 0 && !blocked.Load() {
						if _, e := down.Write(buf[:n]); e != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptedDone
		mu.Lock()
		for _, c := range connections {
			_ = c.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return listener.Addr().String(), blocked
}
func redisBlackholeConfig(t *testing.T, addr string) *config.Config {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return &config.Config{Redis: config.RedisConfig{Host: host, Port: port, DialTimeoutSeconds: 30, ReadTimeoutSeconds: 30, WriteTimeoutSeconds: 30, PoolSize: 1, MinIdleConns: 0}}
}
func TestRedisFactoryDeadlineInterruptsBlackholeSocket(t *testing.T) {
	for _, tc := range []struct {
		name              string
		deadline, maximum time.Duration
	}{{"request_D", 150 * time.Millisecond, 900 * time.Millisecond}, {"detached_cleanup", 5 * time.Second, 6500 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			addr, blocked := redisReplyBlackhole(t)
			client := InitRedis(redisBlackholeConfig(t, addr))
			t.Cleanup(func() { _ = client.Close() })
			warm, cancel := context.WithTimeout(context.Background(), time.Second)
			require.NoError(t, client.Ping(warm).Err())
			cancel()
			require.Equal(t, 30*time.Second, client.Options().ReadTimeout)
			require.True(t, client.Options().ContextTimeoutEnabled)
			blocked.Store(true)
			ctx, stop := context.WithTimeout(context.Background(), tc.deadline)
			defer stop()
			started := time.Now()
			err := client.Get(ctx, "never-returned").Err()
			elapsed := time.Since(started)
			require.Error(t, err)
			require.GreaterOrEqual(t, elapsed, tc.deadline/2)
			require.Less(t, elapsed, tc.maximum)
			t.Logf("blackhole_socket deadline=%s configured_read_timeout=30s elapsed=%s error=%v", tc.deadline, elapsed, err)
		})
	}
}

func TestRedisBlackholeNegativeControlWithoutContextDeadline(t *testing.T) {
	addr, blocked := redisReplyBlackhole(t)
	opts := buildRedisOptions(redisBlackholeConfig(t, addr))
	opts.ContextTimeoutEnabled = false
	opts.ReadTimeout = 400 * time.Millisecond
	opts.MaxRetries = -1
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	blocked.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.Get(ctx, "never-returned").Err()
	elapsed := time.Since(started)
	require.Error(t, err)
	require.GreaterOrEqual(t, elapsed, 250*time.Millisecond)
	require.Less(t, elapsed, 1200*time.Millisecond)
	t.Logf("negative_control context=50ms read_timeout=400ms elapsed=%s error=%v", elapsed, err)
}
