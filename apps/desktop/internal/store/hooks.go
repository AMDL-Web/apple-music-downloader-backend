package store

import (
	"context"
	"slices"
	"time"

	"amdl/internal/client"
)

// HookTest is the last test of one webhook.
type HookTest struct {
	Running bool
	client.HookTest
	At time.Time
}

// SetHooksEnabled sets hooks.yaml's master switch.
func (s *Store) SetHooksEnabled(on bool) {
	c, ctx := s.client, s.ctx
	if c == nil {
		return
	}
	if h := s.Server.Hooks; h != nil {
		h.Enabled = on
	}
	go func() {
		h, err := c.SetHooksEnabled(ctx, on)
		s.postIf(ctx, func() {
			if err != nil {
				s.setNotice("Hooks did not change: "+Reason(err), true)
				s.refreshServer()
				return
			}
			s.Server.Hooks = &h
		})
	}()
}

// PutHook writes the webhook name, then calls done on the UI thread with
// the backend's reason when it refused, "" when it took the write.
func (s *Store) PutHook(name string, w client.HookWrite, done func(reason string)) {
	c, ctx := s.client, s.ctx
	if c == nil {
		return
	}
	go func() {
		e, err := c.PutHook(ctx, name, w)
		s.postIf(ctx, func() {
			if err != nil {
				done(Reason(err))
				return
			}
			h := s.Server.Hooks
			if h == nil {
				h = &client.Hooks{}
				s.Server.Hooks = h
			}
			if i := slices.IndexFunc(h.Entries, func(x client.HookEntry) bool { return x.Name == name }); i >= 0 {
				h.Entries[i] = e
			} else {
				h.Entries = append(h.Entries, e)
			}
			done("")
		})
	}()
}

// SwitchHook turns the webhook name on or off, shown at once.
func (s *Store) SwitchHook(name string, on bool) {
	if h := s.Server.Hooks; h != nil {
		if i := slices.IndexFunc(h.Entries, func(x client.HookEntry) bool { return x.Name == name }); i >= 0 {
			h.Entries[i].Enabled = on
		}
	}
	s.PutHook(name, client.HookWrite{Enabled: &on}, func(reason string) {
		if reason != "" {
			s.setNotice(name+" did not change: "+reason, true)
			s.refreshServer()
		}
	})
}

// DeleteHook removes the webhook name.
func (s *Store) DeleteHook(name string) {
	c, ctx := s.client, s.ctx
	if c == nil {
		return
	}
	go func() {
		err := c.DeleteHook(ctx, name)
		s.postIf(ctx, func() {
			if err != nil {
				s.setNotice(name+" was not deleted: "+Reason(err), true)
				return
			}
			if h := s.Server.Hooks; h != nil {
				h.Entries = slices.DeleteFunc(h.Entries, func(x client.HookEntry) bool { return x.Name == name })
			}
			delete(s.HookTests, name)
			s.setNotice("Deleted "+name, false)
		})
	}()
}

// TestHook sends the webhook name once; HookTests holds the outcome.
func (s *Store) TestHook(name string) {
	c, ctx := s.client, s.ctx
	if c == nil || s.HookTests[name].Running {
		return
	}
	if s.HookTests == nil {
		s.HookTests = map[string]HookTest{}
	}
	s.HookTests[name] = HookTest{Running: true, At: s.now()}
	go func() {
		// The backend bounds the send itself; this bounds the answer.
		wait, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		r, err := c.TestHook(wait, name)
		if err != nil {
			r = client.HookTest{Error: Reason(err)}
		}
		s.postIf(ctx, func() {
			s.HookTests[name] = HookTest{HookTest: r, At: s.now()}
			if !r.Succeeded {
				// The row has room for the start of the reason only.
				s.setNotice("Test of "+name+" failed: "+r.Error, true)
			}
		})
	}()
}
