package main

import (
	"encoding/binary"
	"fmt"
	"github.com/sirupsen/logrus"
	"io"
	"net"
	"sync"
	"time"
)

const (
	defaultId   = "0"
	prefetchKey = "skd://itunes.apple.com/P000000000/s1/e1"
	timeout     = 5 * time.Second
	maxPoolSize = 10
)

type decryptConn struct {
	conn        net.Conn
	lastAdamId  string
	lastKey     string
}

type DecryptInstance struct {
	id             string
	region         string
	decryptPort    int
	pool           []*decryptConn
	poolMu         sync.Mutex
	poolCond       *sync.Cond
	activeCount    int
	isClosed       bool
	stateMu        sync.RWMutex
	LastAdamId     string
	LastKey        string
	LastHandleTime time.Time
	closeOnce      sync.Once
	Available      bool
}

func NewDecryptInstance(inst *WrapperInstance) (*DecryptInstance, error) {
	instance := &DecryptInstance{
		id:             inst.Id,
		region:         inst.Region,
		decryptPort:    inst.DecryptPort,
		pool:           make([]*decryptConn, 0, maxPoolSize),
		LastAdamId:     "",
		LastKey:        "",
		LastHandleTime: time.Time{},
		Available:      true,
	}
	instance.poolCond = sync.NewCond(&instance.poolMu)

	// Pre-warm with one connection to verify it works
	conn, err := instance.getOrCreateConn(prefetchKey)
	if err != nil {
		return nil, err
	}
	instance.releaseConn(conn)

	return instance, nil
}

func (d *DecryptInstance) getOrCreateConn(taskKey string) (*decryptConn, error) {
	d.poolMu.Lock()

	for {
		if d.isClosed {
			d.poolMu.Unlock()
			return nil, fmt.Errorf("instance is closed")
		}

		// 1. Try to find an idle connection with the same key
		for i, c := range d.pool {
			if c.lastKey == taskKey {
				// Remove from pool
				d.pool = append(d.pool[:i], d.pool[i+1:]...)
				d.poolMu.Unlock()
				return c, nil
			}
		}

		// 2. Try to find any idle connection
		if len(d.pool) > 0 {
			c := d.pool[len(d.pool)-1]
			d.pool = d.pool[:len(d.pool)-1]
			d.poolMu.Unlock()
			return c, nil
		}

		// 3. If pool is not full, dial a new connection
		if d.activeCount < maxPoolSize {
			d.activeCount++
			d.poolMu.Unlock()

			// Dial outside the lock
			rawConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", d.decryptPort))
			if err != nil {
				d.poolMu.Lock()
				d.activeCount--
				d.poolCond.Broadcast()
				d.poolMu.Unlock()
				return nil, err
			}

			c := &decryptConn{
				conn: rawConn,
			}
			return c, nil
		}

		// 4. Wait for a connection to be released
		d.poolCond.Wait()
	}
}

func (d *DecryptInstance) releaseConn(c *decryptConn) {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()

	if d.isClosed {
		_ = c.conn.Close()
		d.activeCount--
		d.poolCond.Broadcast()
		return
	}

	d.pool = append(d.pool, c)
	d.poolCond.Signal()
}

func (d *DecryptInstance) discardConn(c *decryptConn) {
	d.poolMu.Lock()
	defer d.poolMu.Unlock()

	_ = c.conn.Close()
	d.activeCount--
	d.poolCond.Broadcast()
}

func (d *DecryptInstance) Unavailable() {
	d.closeOnce.Do(func() {
		d.poolMu.Lock()
		d.isClosed = true
		// Close all idle connections in the pool
		for _, c := range d.pool {
			_ = c.conn.Close()
		}
		d.pool = nil
		d.activeCount = 0
		d.poolCond.Broadcast()
		d.poolMu.Unlock()

		err := KillWrapper(d.id)
		if err != nil {
			logrus.Errorf("failed to kill instance %s: %s", d.id, err)
		}
	})
}

func (d *DecryptInstance) GetLastAdamId() string {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	return d.LastAdamId
}

func (d *DecryptInstance) GetLastHandleTime() time.Time {
	d.stateMu.RLock()
	defer d.stateMu.RUnlock()
	return d.LastHandleTime
}

func (d *DecryptInstance) Process(task *Task) {
	d.stateMu.Lock()
	d.LastHandleTime = time.Now()
	d.stateMu.Unlock()

	c, err := d.getOrCreateConn(task.Key)
	if err != nil {
		d.Unavailable()
		task.Result <- &Result{
			Success: false,
			Data:    task.Payload,
			Error:   err,
		}
		return
	}

	if c.lastKey == "" || c.lastKey != task.Key {
		err := d.switchConnContext(c, task.AdamId, task.Key)
		if err != nil {
			d.discardConn(c)
			d.Unavailable()
			task.Result <- &Result{
				Success: false,
				Data:    task.Payload,
				Error:   err,
			}
			return
		}
	}

	result, err := d.decryptConn(c, task.Payload)
	if err != nil {
		d.discardConn(c)
		d.Unavailable()
		task.Result <- &Result{
			Success: false,
			Data:    task.Payload,
			Error:   err,
		}
		return
	}

	// Update global instance keys for heuristic status
	d.stateMu.Lock()
	d.LastAdamId = task.AdamId
	d.LastKey = task.Key
	d.stateMu.Unlock()

	d.releaseConn(c)

	task.Result <- &Result{
		Success: true,
		Data:    result,
		Error:   nil,
	}
}

func (d *DecryptInstance) decryptConn(c *decryptConn, sample []byte) ([]byte, error) {
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer c.conn.SetDeadline(time.Time{})
	err := binary.Write(c.conn, binary.LittleEndian, uint32(len(sample)))
	if err != nil {
		return nil, err
	}
	_, err = c.conn.Write(sample)
	if err != nil {
		return nil, err
	}
	de := make([]byte, len(sample))
	_, err = io.ReadFull(c.conn, de)
	if err != nil {
		return nil, err
	}
	return de, nil
}

func (d *DecryptInstance) switchConnContext(c *decryptConn, adamId string, key string) error {
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer c.conn.SetDeadline(time.Time{})
	if c.lastKey != "" {
		_, err := c.conn.Write([]byte{0, 0, 0, 0})
		if err != nil {
			return err
		}
	}
	if key == prefetchKey {
		_, err := c.conn.Write([]byte{byte(len(defaultId))})
		if err != nil {
			return err
		}
		_, err = io.WriteString(c.conn, defaultId)
		if err != nil {
			return err
		}
	} else {
		_, err := c.conn.Write([]byte{byte(len(adamId))})
		if err != nil {
			return err
		}
		_, err = io.WriteString(c.conn, adamId)
		if err != nil {
			return err
		}
	}
	_, err := c.conn.Write([]byte{byte(len(key))})
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.conn, key)
	if err != nil {
		return err
	}

	c.lastAdamId = adamId
	c.lastKey = key
	return nil
}
