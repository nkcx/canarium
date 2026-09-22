package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/nkcx/canarium/internal/conditions"
	"github.com/nkcx/canarium/internal/config"
)

// planInfo is a plan as the UI needs to show it: what triggers it, what it
// would shut down and in what order, and what brings everything back --
// with every condition's current evaluation alongside.
//
// The endpoint used to return a plan's name and its stage count, and
// nothing else. The Plans page had nothing to show, and the guide's advice
// to "check the trigger condition in the web UI -- it shows the current
// evaluation state" pointed at a feature that did not exist.
type planInfo struct {
	Name string `json:"name"`

	// Stages is the stage count, kept for API consumers that read it
	// before the detail below existed.
	Stages int `json:"stages"`

	Trigger  conditions.Explanation  `json:"trigger"`
	Abort    *conditions.Explanation `json:"abort,omitempty"`
	Shutdown []stageInfo             `json:"shutdown"`

	PostShutdown *postShutdownInfo `json:"post_shutdown,omitempty"`
	Wake         wakeInfo          `json:"wake"`
}

type stageInfo struct {
	Name string `json:"name"`

	// Clients is what the stage's references resolve to right now.
	Clients []string `json:"clients"`

	// Refs is the stage's references as written, so `tag:compute` can be
	// shown alongside the machines it currently means.
	Refs []string `json:"refs"`

	// Unmatched lists references that resolve to nothing. A stage whose
	// every reference is unmatched will run and shut nothing down, which
	// validation warns about and the UI should too.
	Unmatched []string `json:"unmatched,omitempty"`

	When            conditions.Explanation `json:"when"`
	Budget          string                 `json:"budget,omitempty"`
	WaitTimeout     string                 `json:"wait_timeout,omitempty"`
	WaitPolicy      string                 `json:"wait_policy,omitempty"`
	PointOfNoReturn bool                   `json:"point_of_no_return"`
}

// postShutdownInfo deliberately omits the NUT credentials the config
// carries.
type postShutdownInfo struct {
	Action  string `json:"action"`
	Command string `json:"command,omitempty"`
	Delay   int    `json:"delay"`
	UPS     string `json:"ups,omitempty"`
}

type wakeInfo struct {
	Gate          conditions.Explanation `json:"gate"`
	GateMissing   bool                   `json:"gate_missing"`
	Order         string                 `json:"order,omitempty"`
	Stagger       string                 `json:"stagger,omitempty"`
	ProbeInterval string                 `json:"probe_interval,omitempty"`
	BootDeadline  string                 `json:"boot_deadline,omitempty"`
	Retries       int                    `json:"retries,omitempty"`
}

func (s *Server) handlePlans(w http.ResponseWriter, r *http.Request) {
	now := time.Now()

	// An empty list, not null: a JSON consumer iterating the response
	// should not need to special-case "no plans".
	plans := make([]planInfo, 0, len(s.cfg.Plans))

	for i := range s.cfg.Plans {
		p := &s.cfg.Plans[i]

		info := planInfo{
			Name:     p.Name,
			Stages:   len(p.Shutdown.Stages),
			Trigger:  s.executor.ExplainCondition(&p.Trigger, now),
			Shutdown: make([]stageInfo, 0, len(p.Shutdown.Stages)),
			Wake: wakeInfo{
				Gate:          s.executor.ExplainCondition(&p.Wake.Gate, now),
				GateMissing:   config.IsZeroCondition(p.Wake.Gate),
				Order:         p.Wake.Order,
				Stagger:       p.Wake.Stagger,
				ProbeInterval: p.Wake.ProbeInterval,
				BootDeadline:  p.Wake.BootDeadline,
				Retries:       p.Wake.Retries,
			},
		}

		if p.Abort != nil {
			abort := s.executor.ExplainCondition(p.Abort, now)
			info.Abort = &abort
		}

		for j := range p.Shutdown.Stages {
			st := &p.Shutdown.Stages[j]
			info.Shutdown = append(info.Shutdown, stageInfo{
				Name:            st.Name,
				Clients:         nonNil(config.ResolveClientRefs(st.Clients, s.cfg)),
				Refs:            nonNil(st.Clients),
				Unmatched:       s.unmatchedRefs(st.Clients),
				When:            s.executor.ExplainCondition(&st.When, now),
				Budget:          st.Budget,
				WaitTimeout:     st.WaitTimeout,
				WaitPolicy:      st.WaitPolicy,
				PointOfNoReturn: st.PointOfNoReturn,
			})
		}

		if ps := p.Shutdown.PostShutdown; ps != nil {
			info.PostShutdown = &postShutdownInfo{
				Action: ps.Action, Command: ps.Command, Delay: ps.Delay, UPS: ps.UPS,
			}
		}

		plans = append(plans, info)
	}

	s.writeJSON(w, http.StatusOK, plans)
}

// unmatchedRefs returns the references that resolve to no client.
func (s *Server) unmatchedRefs(refs []string) []string {
	var out []string
	for _, ref := range refs {
		if strings.HasPrefix(ref, "tag:") {
			if len(config.ResolveClientRefs([]string{ref}, s.cfg)) == 0 {
				out = append(out, ref)
			}
			continue
		}
		// ResolveClientRefs passes bare names through unchecked, so a
		// misspelled client name has to be caught here.
		if !s.clientConfigured(ref) {
			out = append(out, ref)
		}
	}
	return out
}

func (s *Server) clientConfigured(name string) bool {
	for _, c := range s.cfg.Clients {
		if c.Name == name {
			return true
		}
	}
	return false
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
