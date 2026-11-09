package board

// lessons.go: the lessons of the task agent inside the board sync (internal/lessons): the admin's review requests
// from /admin/lessons, the daily decay, and the proposed lessons and loaded-lesson uses of every intake or run.

import (
	"errors"

	"github.com/sinaneysi/argus/internal/lessons"
)

func (r *run) lessonOpts() *lessons.Options {
	return &lessons.Options{Allow: r.o.Allow, KnowledgeDir: r.o.KnowledgeDir, Now: r.o.Now, Log: r.o.Log}
}

func (r *run) lessonBoard() lessons.Board {
	return lessons.Board{Event: func(kind, actor, text string) { r.event(nil, kind, actor, text) }, SetState: r.setState}
}

// lessonHandle applies a "lesson" or "lesson-settings" request.
func (r *run) lessonHandle(req map[string]any, by, role string) (string, any) {
	if err := r.begin(); err != nil {
		panic(dbErr{err})
	}
	res, err := r.lessonOpts().Handle(r.ctx, r.tx, req, by, role, r.lessonBoard())
	var ref lessons.Refused
	if errors.As(err, &ref) {
		panic(refused(string(ref)))
	}
	if err != nil {
		panic(dbErr{err})
	}
	return res, nil
}

// lessonTick is the daily decay and the generated files.
func (r *run) lessonTick() {
	if err := r.begin(); err != nil {
		panic(dbErr{err})
	}
	if _, err := r.lessonOpts().Tick(r.ctx, r.tx, r.getState, r.setState); err != nil {
		r.rollback()
		r.log().Warn("lessons", "error", head(err.Error(), 200))
		return
	}
	r.commit()
}

// lessonsAfter counts the lessons a model call loaded and stores the ones it proposed; never fails the task.
func (r *run) lessonsAfter(key string, d map[string]any, idsPath string, runID any) {
	if d == nil {
		d = map[string]any{}
	}
	if err := r.begin(); err != nil {
		panic(dbErr{err})
	}
	line, err := r.lessonOpts().AfterRun(r.ctx, r.tx, key, d, idsPath, runID)
	if err != nil {
		r.alog(key, "error", "lessons: "+head(err.Error(), 200))
		return
	}
	if line != "" {
		r.alog(key, "lessons", line)
	}
}
