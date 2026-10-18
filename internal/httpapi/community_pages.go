package httpapi

import (
	"crypto/rand"
	_ "embed"
	"html/template"
	"net/http"
	"pvzce-site/internal/workshop"
	"strconv"
)

//go:embed community.html
var communityHTML string
var communityTemplate = template.Must(template.New("community").Funcs(template.FuncMap{"pretty": jsonPretty}).Parse(communityHTML))

func registerCommunityPages(mux *http.ServeMux, deps Dependencies, public, protect func(string, http.HandlerFunc)) {
	s := deps.Workshop
	public("GET /community", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		view := q.Get("view")
		if view == "" {
			view = "feedback"
		}
		uid := requestUser(r)
		id, _ := strconv.ParseInt(q.Get("id"), 10, 64)
		pid, _ := strconv.ParseInt(q.Get("project_id"), 10, 64)
		p := communityPageInput(r)
		if uid == 0 && view != "comments" && view != "feedback" {
			writeError(w, r, 401, "login_required", "Please log in")
			return
		}
		var d any
		var e error
		switch view {
		case "comments":
			var comments workshop.Page[workshop.Comment]
			comments, e = s.Comments(r.Context(), uid, pid, p)
			if e == nil {
				var ratings any
				ratings, e = s.RatingSummary(r.Context(), uid, pid, p.IncludeSuggestive)
				d = map[string]any{"comments": comments, "ratings": ratings}
			}
		case "feedback":
			if id > 0 {
				d, e = s.FeedbackDetail(r.Context(), uid, id, p.IncludeSuggestive)
			} else {
				d, e = s.FeedbackList(r.Context(), uid, p)
			}
		case "reports":
			if id > 0 {
				d, e = s.CaseDetail(r.Context(), uid, id)
			} else {
				d, e = s.Cases(r.Context(), uid, p)
			}
		case "queue":
			d, e = s.CommunityQueue(r.Context(), uid, p)
		case "statistics":
			d, e = s.Statistics(r.Context(), uid, pid, q.Get("all") == "1")
		case "accounts":
			d, e = s.Accounts(r.Context(), uid, p)
		case "audit":
			d, e = s.AuditPage(r.Context(), uid, p, q.Get("action"))
		case "notifications":
			d, e = s.Notifications(r.Context(), uid, p)
		case "appeals":
			d, e = s.ContentAppeals(r.Context(), uid, p)
		case "history":
			d, e = s.CommentHistoryBefore(r.Context(), uid, id, p.Before)
		default:
			e = workshop.ErrInvalid
		}
		if e != nil {
			businessError(w, r, e)
			return
		}
		session, _ := Account(r.Context())
		authHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = communityTemplate.Execute(w, map[string]any{"View": view, "ID": id, "ProjectID": pid, "Session": session, "Data": d, "Key": rand.Text() + rand.Text(), "Before": p.Before})
	})
	protect("POST /community/actions", func(w http.ResponseWriter, r *http.Request) {
		uid := requestUser(r)
		id := formInt(r, "id")
		pid := formInt(r, "project_id")
		job := r.FormValue("job")
		location := "/community?view=" + r.FormValue("view")
		var e error
		review := workshop.ReviewInput{Action: r.FormValue("action"), Reason: r.FormValue("reason"), Category: r.FormValue("category")}
		contentReview := workshop.ContentReview{ReviewInput: review, Revision: formInt(r, "revision")}
		switch job {
		case "comment.create":
			_, e = s.CreateComment(r.Context(), uid, pid, workshop.CommentInput{Body: r.FormValue("body"), ContentLabel: r.FormValue("content_label")})
		case "comment.edit":
			_, e = s.EditComment(r.Context(), uid, id, workshop.CommentInput{Body: r.FormValue("body"), ContentLabel: r.FormValue("content_label"), Revision: formInt(r, "revision")})
		case "comment.delete":
			e = s.DeleteComment(r.Context(), uid, id)
		case "comment.review":
			e = s.ReviewComment(r.Context(), uid, id, contentReview)
		case "rating.set":
			e = s.SetRating(r.Context(), uid, pid, int(formInt(r, "score")))
		case "rating.delete":
			e = s.DeleteRating(r.Context(), uid, pid)
		case "rating.void":
			e = s.VoidRating(r.Context(), uid, pid, formInt(r, "user_id"), r.FormValue("reason"))
		case "feedback.create":
			var f workshop.Feedback
			f, e = s.CreateFeedback(r.Context(), uid, workshop.FeedbackInput{ProjectID: pid, VersionID: formInt(r, "version_id"), GameVersion: r.FormValue("game_version"), Kind: r.FormValue("kind"), Title: r.FormValue("title"), Steps: r.FormValue("steps"), Expected: r.FormValue("expected"), Actual: r.FormValue("actual"), Public: r.FormValue("public") == "1", ContentLabel: r.FormValue("content_label")})
			location = "/community?view=feedback&id=" + strconv.FormatInt(f.ID, 10)
		case "feedback.supplement":
			e = s.SupplementFeedback(r.Context(), uid, id, r.FormValue("reply"))
			location += "&id=" + strconv.FormatInt(id, 10)
		case "feedback.process":
			e = s.ProcessFeedback(r.Context(), uid, id, workshop.ProcessInput{State: r.FormValue("state"), Reply: r.FormValue("reply")})
		case "feedback.review":
			e = s.ReviewFeedback(r.Context(), uid, id, contentReview)
		case "file.review":
			e = s.ReviewAttachment(r.Context(), uid, id, review)
		case "file.clear_safety":
			e = s.ClearSafety(r.Context(), uid, id, r.FormValue("reason"))
		case "report.create":
			var cid int64
			var files []int64
			for _, v := range r.Form["file_id"] {
				fid, _ := strconv.ParseInt(v, 10, 64)
				if fid > 0 {
					files = append(files, fid)
				}
			}
			cid, e = s.Report(r.Context(), uid, workshop.ReportInput{Kind: r.FormValue("target_kind"), TargetID: formInt(r, "target_id"), Reason: r.FormValue("reason"), Evidence: r.FormValue("evidence"), FileIDs: files})
			location = "/community?view=reports&id=" + strconv.FormatInt(cid, 10)
		case "report.claim":
			e = s.ClaimCase(r.Context(), uid, id)
		case "report.resolve":
			e = s.ResolveCase(r.Context(), uid, id, workshop.ResolveCaseInput{Action: review.Action, Reason: review.Reason, Category: review.Category})
		case "report.appeal":
			_, e = s.AppealCase(r.Context(), uid, id, r.FormValue("reason"))
		case "report.appeal_resolve":
			e = s.ResolveCaseAppeal(r.Context(), uid, id, r.FormValue("outcome"), r.FormValue("reply"))
		case "content.appeal":
			_, e = s.AppealContent(r.Context(), uid, formInt(r, "action_id"), r.FormValue("reason"))
		case "content.appeal_resolve":
			e = s.ResolveContentAppeal(r.Context(), uid, id, r.FormValue("reply"))
		case "notification.read":
			e = s.ReadNotification(r.Context(), uid, id)
		case "account.manage":
			e = s.ManageAccount(r.Context(), uid, formInt(r, "user_id"), r.FormValue("role"), r.FormValue("status"), r.FormValue("reason"))
		default:
			e = workshop.ErrInvalid
		}
		if job == "comment.create" || job == "rating.set" || job == "rating.delete" {
			location = "/community?view=comments&project_id=" + strconv.FormatInt(pid, 10)
		}
		pageDone(w, r, location, e)
	})
}
