package federation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tvt/logger"
	"tvt/store"
)

var commentRefRe = regexp.MustCompile(`^c:[0-9]{1,18}$`)

const (
	maxCommentBody = 2000
	renewBatch     = 25
)

func cleanBody(s string) string {
	s = strings.TrimSpace(stripFormat(s))
	if r := []rune(s); len(r) > maxCommentBody {
		s = string(r[:maxCommentBody])
	}
	return s
}

func parseStamp(v string) string {
	if _, err := time.Parse("2006-01-02 15:04:05", v); err != nil {
		return store.Now()
	}
	return v
}

// onComment keeps a leased copy of an episode comment from an allow-listed
// server. The copy is shown next to the local discussion of that episode.
func (s *Service) onComment(host string, m message) error {
	c := m.Comment
	if c == nil || !commentRefRe.MatchString(c.Ref) || (c.Parent != "" && !commentRefRe.MatchString(c.Parent)) || c.ShowTMDB == 0 {
		return errBadMessage
	}
	if shared, err := s.Store.PeerShares(host); err != nil || !shared {
		return err
	}
	body := cleanBody(c.Body)
	if body == "" {
		return errBadMessage
	}
	shadow, err := s.Store.EnsureRemoteUser(host, m.From.Username, cleanName(m.From.Name))
	if err != nil {
		return err
	}
	if shadow.IsDeactivated {
		return nil
	}
	edited := ""
	if c.Edited != "" {
		edited = parseStamp(c.Edited)
	}
	return s.Store.SaveRemoteComment(store.RemoteComment{
		UserID: shadow.ID, Host: host, Ref: c.Ref, ParentRef: c.Parent, ShowTMDB: c.ShowTMDB,
		Season: clampInt(c.Season, 0, 5000), Episode: clampInt(c.Episode, 0, 100000),
		Body: body, Spoiler: c.Spoiler, CreatedAt: parseStamp(c.Created), Edited: edited,
	})
}

// onCommentRetract drops a copy when its author withdraws the comment.
func (s *Service) onCommentRetract(host string, m message) error {
	if !commentRefRe.MatchString(m.Ref) {
		return errBadMessage
	}
	shadow, err := s.Store.UserByUsername(store.RemoteHandle(m.From.Username, host))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	author, err := s.Store.RemoteCommentAuthor(host, m.Ref)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if author != shadow.ID {
		return errBadMessage
	}
	return s.Store.DeleteRemoteComment(host, m.Ref)
}

// onReport files a report made on another server against one of our comments,
// so our moderators see it in their queue like any other.
func (s *Service) onReport(host string, m message) error {
	if !commentRefRe.MatchString(m.Ref) {
		return errBadMessage
	}
	if shared, err := s.Store.PeerShares(host); err != nil || !shared {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(m.Ref, "c:"), 10, 64)
	if err != nil {
		return errBadMessage
	}
	if _, err := s.Store.OpenEpisodeComment(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	reporter, err := s.Store.EnsureRemoteUser(host, m.From.Username, cleanName(m.From.Name))
	if err != nil {
		return err
	}
	reason := clip(m.Reason, 500)
	if err := s.Store.ReportComment(reporter.ID, id, "abuse", reason); err != nil && !errors.Is(err, store.ErrAlreadyReported) {
		return err
	}
	return nil
}

type renewRequest struct {
	Refs []string `json:"refs"`
	// Nonce makes two otherwise identical requests sign differently, so the
	// receiver's replay check never mistakes a fresh renewal for a replay.
	Nonce string `json:"nonce"`
}

type renewResult struct {
	Ref     string      `json:"ref"`
	Status  string      `json:"status"`
	Comment *CommentMsg `json:"comment,omitempty"`
}

type renewResponse struct {
	Results []renewResult `json:"results"`
}

// CommentMessage builds the shared form of a local comment.
func CommentMessage(c *store.OpenComment) CommentMsg {
	m := CommentMsg{Ref: "c:" + strconv.FormatInt(c.ID, 10), ShowTMDB: c.ShowTMDB, Season: c.Season, Episode: c.Episode,
		Body: c.Body, Spoiler: c.Spoiler, Created: c.CreatedAt, Edited: c.EditedAt}
	if c.ParentID != 0 {
		m.Parent = "c:" + strconv.FormatInt(c.ParentID, 10)
	}
	return m
}

// Renew answers another server that asks whether comments it holds are still
// shown here. A comment that was deleted, hidden or withdrawn answers "gone",
// the equivalent of 410, so deletions reach every copy within one lease.
func (s *Service) Renew(w http.ResponseWriter, r *http.Request) {
	host, body, ok := s.verify(w, r)
	if !ok {
		return
	}
	if shared, err := s.Store.PeerShares(host); err != nil {
		logger.Error("FEDERATION renew from %s: %v", host, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	} else if !shared {
		http.Error(w, "not allow-listed", http.StatusForbidden)
		return
	}
	var req renewRequest
	if err := json.Unmarshal(body, &req); err != nil || len(req.Refs) == 0 || len(req.Refs) > renewBatch {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var out renewResponse
	for _, ref := range req.Refs {
		res := renewResult{Ref: ref, Status: "gone"}
		if commentRefRe.MatchString(ref) {
			id, _ := strconv.ParseInt(strings.TrimPrefix(ref, "c:"), 10, 64)
			c, err := s.Store.OpenEpisodeComment(id)
			switch {
			case err == nil:
				u, uerr := s.Store.UserByID(c.UserID)
				if uerr == nil && u.ShareActivity {
					m := CommentMessage(c)
					res.Status, res.Comment = "ok", &m
				}
			case errors.Is(err, store.ErrNotFound):
			default:
				logger.Error("FEDERATION renew lookup %s: %v", ref, err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		out.Results = append(out.Results, res)
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		logger.Warn("FEDERATION renew reply: %v", err)
	}
}

// RenewLeases asks each home server to confirm the copies that are about to
// lapse. Confirmed copies get a fresh lease (and any edit); copies the server
// no longer shows are deleted; unreachable servers are left to lapse on their own.
func (s *Service) RenewLeases(ctx context.Context) {
	due, err := s.Store.DueRenewals(8*time.Hour, 500)
	if err != nil {
		logger.Error("FEDERATION %v", err)
		return
	}
	byHost := map[string][]store.RemoteComment{}
	var order []string
	for _, c := range due {
		if _, ok := byHost[c.Host]; !ok {
			order = append(order, c.Host)
		}
		byHost[c.Host] = append(byHost[c.Host], c)
	}
	for _, host := range order {
		if ctx.Err() != nil {
			return
		}
		if shared, err := s.Store.PeerShares(host); err != nil {
			logger.Error("FEDERATION renew %s: %v", host, err)
			continue
		} else if !shared {
			for _, c := range byHost[host] {
				if err := s.Store.DeleteRemoteComment(host, c.Ref); err != nil {
					logger.Error("FEDERATION %v", err)
				}
			}
			continue
		}
		items := byHost[host]
		for start := 0; start < len(items); start += renewBatch {
			end := start + renewBatch
			if end > len(items) {
				end = len(items)
			}
			s.renewBatch(ctx, host, items[start:end])
		}
	}
}

func (s *Service) renewBatch(ctx context.Context, host string, items []store.RemoteComment) {
	refs := make([]string, 0, len(items))
	for _, c := range items {
		refs = append(refs, c.Ref)
	}
	payload, err := json.Marshal(renewRequest{Refs: refs, Nonce: newID()})
	if err != nil {
		logger.Error("FEDERATION encode renewal for %s: %v", host, err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, reply, err := s.call(cctx, host, "/federation/renew", payload)
	if err != nil || code != http.StatusOK {
		logger.Warn("FEDERATION could not renew %d comments with %s (status %d): %v", len(refs), host, code, err)
		return
	}
	var resp renewResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		logger.Warn("FEDERATION bad renewal reply from %s: %v", host, err)
		return
	}
	for _, res := range resp.Results {
		switch {
		case res.Status == "ok" && res.Comment != nil && res.Comment.Ref == res.Ref:
			edited := ""
			if res.Comment.Edited != "" {
				edited = parseStamp(res.Comment.Edited)
			}
			if body := cleanBody(res.Comment.Body); body != "" {
				if err := s.Store.RenewRemoteComment(host, res.Ref, body, res.Comment.Spoiler, edited); err != nil {
					logger.Error("FEDERATION %v", err)
				}
			}
		case res.Status == "gone":
			if err := s.Store.DeleteRemoteComment(host, res.Ref); err != nil {
				logger.Error("FEDERATION %v", err)
			}
		}
	}
}
