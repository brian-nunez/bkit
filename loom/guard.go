package loom

// ending says how a call into application code finished.
type ending int

const (
	returned ending = iota // the function returned normally
	panicked               // the function panicked
	exited                 // the function called runtime.Goexit
)

// isolate runs f on its own goroutine and waits for it. A panic or
// runtime.Goexit in f ends only that goroutine; isolate reports which
// happened, with the recovered panic value.
func isolate(f func()) (end ending, value any) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		normal := false
		defer func() {
			if normal {
				return
			}
			if r := recover(); r != nil {
				end, value = panicked, r
				return
			}
			end = exited
		}()
		f()
		normal = true
	}()
	<-done
	return end, value
}
