package httpapi

import (
	"net/http"
	"pvzce-site/internal/workshop"
	"strconv"
	"time"
)

func communityBody[T any](fn func(*http.Request, T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in T
		if !businessBody(w, r, &in) {
			return
		}
		d, e := fn(r, in)
		businessResult(w, r, d, e)
	}
}
func communityPageInput(r *http.Request) workshop.PageInput {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	size, _ := strconv.Atoi(q.Get("size"))
	return workshop.PageInput{Before: before, Size: size, Mine: q.Get("mine") == "1", IncludeSuggestive: q.Get("include_suggestive") == "1"}
}
func registerCommunity(mux *http.ServeMux, deps Dependencies, public, protect func(string, http.HandlerFunc)) {
	s := deps.Workshop
	public("GET /api/v1/projects/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Comments(r.Context(), requestUser(r), pathID(r, "id"), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/projects/{id}/comments", communityBody(func(r *http.Request, in workshop.CommentInput) (any, error) {
		return s.CreateComment(r.Context(), requestUser(r), pathID(r, "id"), in)
	}))
	protect("PATCH /api/v1/comments/{id}", communityBody(func(r *http.Request, in workshop.CommentInput) (any, error) {
		return s.EditComment(r.Context(), requestUser(r), pathID(r, "id"), in)
	}))
	protect("DELETE /api/v1/comments/{id}", func(w http.ResponseWriter, r *http.Request) {
		businessResult(w, r, nil, s.DeleteComment(r.Context(), requestUser(r), pathID(r, "id")))
	})
	protect("GET /api/v1/comments/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.CommentHistoryBefore(r.Context(), requestUser(r), pathID(r, "id"), communityPageInput(r).Before)
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/comments/{id}/review", communityBody(func(r *http.Request, in workshop.ContentReview) (any, error) {
		return nil, s.ReviewComment(r.Context(), requestUser(r), pathID(r, "id"), in)
	}))
	public("GET /api/v1/projects/{id}/ratings", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.RatingSummary(r.Context(), requestUser(r), pathID(r, "id"), communityPageInput(r).IncludeSuggestive)
		businessResult(w, r, d, e)
	})
	type rating struct {
		Score int `json:"score"`
	}
	protect("PUT /api/v1/projects/{id}/rating", communityBody(func(r *http.Request, in rating) (any, error) {
		return nil, s.SetRating(r.Context(), requestUser(r), pathID(r, "id"), in.Score)
	}))
	protect("DELETE /api/v1/projects/{id}/rating", func(w http.ResponseWriter, r *http.Request) {
		businessResult(w, r, nil, s.DeleteRating(r.Context(), requestUser(r), pathID(r, "id")))
	})
	type reasonInput struct {
		Reason string `json:"reason"`
	}
	protect("POST /api/v1/admin/projects/{id}/ratings/{user}/void", communityBody(func(r *http.Request, in reasonInput) (any, error) {
		return nil, s.VoidRating(r.Context(), requestUser(r), pathID(r, "id"), pathID(r, "user"), in.Reason)
	}))
	public("GET /api/v1/feedback", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.FeedbackList(r.Context(), requestUser(r), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/feedback", communityBody(func(r *http.Request, in workshop.FeedbackInput) (any, error) {
		return s.CreateFeedback(r.Context(), requestUser(r), in)
	}))
	public("GET /api/v1/feedback/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.FeedbackDetail(r.Context(), requestUser(r), pathID(r, "id"), communityPageInput(r).IncludeSuggestive)
		businessResult(w, r, d, e)
	})
	type replyInput struct {
		Reply string `json:"reply"`
	}
	protect("POST /api/v1/feedback/{id}/supplements", communityBody(func(r *http.Request, in replyInput) (any, error) {
		return nil, s.SupplementFeedback(r.Context(), requestUser(r), pathID(r, "id"), in.Reply)
	}))
	protect("POST /api/v1/feedback/{id}/process", communityBody(func(r *http.Request, in workshop.ProcessInput) (any, error) {
		return nil, s.ProcessFeedback(r.Context(), requestUser(r), pathID(r, "id"), in)
	}))
	protect("POST /api/v1/feedback/{id}/review", communityBody(func(r *http.Request, in workshop.ContentReview) (any, error) {
		return nil, s.ReviewFeedback(r.Context(), requestUser(r), pathID(r, "id"), in)
	}))
	protect("PUT /api/v1/feedback/{id}/uploads/attachment", func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength < 1 {
			writeError(w, r, 411, "length_required", "Content-Length required")
			return
		}
		ip, e := s.ClientIP(r)
		if e != nil {
			businessError(w, r, e)
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(s.ReceiveDeadline())
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Minute))
		d, e := s.Upload(r.Context(), requestUser(r), workshop.UploadInput{FeedbackID: pathID(r, "id"), Purpose: "attachment", Filename: r.Header.Get("Upload-Filename"), ExpectedSHA256: r.Header.Get("Upload-SHA256"), IdempotencyKey: r.Header.Get("Idempotency-Key"), IP: ip, Size: r.ContentLength, Public: r.URL.Query().Get("public") == "1", ContentLabel: r.URL.Query().Get("content_label")}, r.Body)
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/files/{file}/review", communityBody(func(r *http.Request, in workshop.ReviewInput) (any, error) {
		return nil, s.ReviewAttachment(r.Context(), requestUser(r), pathID(r, "file"), in)
	}))
	protect("POST /api/v1/files/{file}/clear-safety", communityBody(func(r *http.Request, in reasonInput) (any, error) {
		return nil, s.ClearSafety(r.Context(), requestUser(r), pathID(r, "file"), in.Reason)
	}))
	protect("POST /api/v1/reports", communityBody(func(r *http.Request, in workshop.ReportInput) (any, error) {
		id, e := s.Report(r.Context(), requestUser(r), in)
		return map[string]int64{"id": id}, e
	}))
	protect("GET /api/v1/reports", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Cases(r.Context(), requestUser(r), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("GET /api/v1/reports/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.CaseDetail(r.Context(), requestUser(r), pathID(r, "id"))
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/reports/{id}/claim", func(w http.ResponseWriter, r *http.Request) {
		businessResult(w, r, nil, s.ClaimCase(r.Context(), requestUser(r), pathID(r, "id")))
	})
	protect("POST /api/v1/reports/{id}/resolve", communityBody(func(r *http.Request, in workshop.ResolveCaseInput) (any, error) {
		return nil, s.ResolveCase(r.Context(), requestUser(r), pathID(r, "id"), in)
	}))
	protect("POST /api/v1/reports/{id}/appeals", communityBody(func(r *http.Request, in reasonInput) (any, error) {
		id, e := s.AppealCase(r.Context(), requestUser(r), pathID(r, "id"), in.Reason)
		return map[string]int64{"id": id}, e
	}))
	type resolution struct {
		Outcome string `json:"outcome"`
		Reply   string `json:"reply"`
	}
	protect("POST /api/v1/report-appeals/{id}/resolve", communityBody(func(r *http.Request, in resolution) (any, error) {
		return nil, s.ResolveCaseAppeal(r.Context(), requestUser(r), pathID(r, "id"), in.Outcome, in.Reply)
	}))
	type contentAppeal struct {
		ActionID int64  `json:"action_id"`
		Reason   string `json:"reason"`
	}
	protect("POST /api/v1/content-appeals", communityBody(func(r *http.Request, in contentAppeal) (any, error) {
		id, e := s.AppealContent(r.Context(), requestUser(r), in.ActionID, in.Reason)
		return map[string]int64{"id": id}, e
	}))
	protect("GET /api/v1/content-appeals", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.ContentAppeals(r.Context(), requestUser(r), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/content-appeals/{id}/resolve", communityBody(func(r *http.Request, in replyInput) (any, error) {
		return nil, s.ResolveContentAppeal(r.Context(), requestUser(r), pathID(r, "id"), in.Reply)
	}))
	protect("GET /api/v1/community/review-queue", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.CommunityQueue(r.Context(), requestUser(r), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("GET /api/v1/admin/accounts", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Accounts(r.Context(), requestUser(r), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("GET /api/v1/statistics", func(w http.ResponseWriter, r *http.Request) {
		pid, _ := strconv.ParseInt(r.URL.Query().Get("project_id"), 10, 64)
		d, e := s.Statistics(r.Context(), requestUser(r), pid, r.URL.Query().Get("all") == "1")
		businessResult(w, r, d, e)
	})
	protect("GET /api/v1/notifications", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Notifications(r.Context(), requestUser(r), communityPageInput(r))
		businessResult(w, r, d, e)
	})
	protect("POST /api/v1/notifications/{id}/read", func(w http.ResponseWriter, r *http.Request) {
		businessResult(w, r, nil, s.ReadNotification(r.Context(), requestUser(r), pathID(r, "id")))
	})
	registerCommunityPages(mux, deps, public, protect)
}
