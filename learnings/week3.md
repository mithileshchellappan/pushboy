GO ROUTINES

- everything is a goroutine, which is a concurrent function exectuion. but the term is mostly used when a new parallel concurrent execution is triggered from the main goroutine.
- init a go routine using go func() {...}() in main.go
- We can create channel to communicate b/w goroutines using chan := make(chan `type`)
- uses .C (Channel) and <-..C (Receiver) to commuinicate b/w routines. Channel acts as a conveyor to place message for other routines to pick it up.
- the <-ticker.C is in an hindsight a async await to wait until a message is received. 
- Goroutines are not idempotent, but the work they do must be idempotentic
- Goroutines must be treated as disposable like with any other worker setup
- "Do not communicate by sharing memory; instead share memory by communicating"