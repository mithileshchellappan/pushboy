GO ROUTINES

- everything is a goroutine, which is a concurrent function exectuion. but the term is mostly used when a new parallel concurrent execution is triggered from the main goroutine.
- init a go routine using go func() {...}() in main.go
- We can create channel to communicate b/w goroutines using chan := make(chan `type`)
- uses .C (Channel) and <-..C (Receiver) to commuinicate b/w routines. Channel acts as a conveyor to place message for other routines to pick it up.
- the <-ticker.C is in an hindsight a async await to wait until a message is received. 
- Goroutines are not idempotent, but the work they do must be idempotentic
- Goroutines must be treated as disposable like with any other worker setup
- "Do not communicate by sharing memory; instead share memory by communicating"
- To assert/ get value of a data in a inherited type as its parent interface use .(new mappin type) 
    - for example, `tokens.Claims` is an inherited type of interface `jwt.Claims`
    - tokens.Claims contians `jwt.MapClaims` which is put in by `jwt.New()`.
    - But we can't directly use `.MapClaims` on `tokens.Claims`, instead we do `tokens.Claims.(jwt.MapClaims)`, which is saying type assert it to a map/
    - basically type casting in ts it would be `const claims = token.Claims as jwt.MapClaims;`
    - one difference is, for Go it is a runtime check, while in TS it is a compile-time check
- `http` package when created a client using `&http.Client()` comes with a default Transport layer and manages all connection pools.
- `json.Marshal` is `JSON.stringify()`