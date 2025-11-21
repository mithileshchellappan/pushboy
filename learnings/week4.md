#Channels
- a go channel is a pipe or conveyor belt with 2 types 
    - unbuffered `make(chan Job)` - synchronous hand-off. sender blocks until receiver takes the item. if no is ready to receive, sender waits forever.
    - buffered `make(chan Job, 100)` - a queue with a specific capacity (100). sender will drop items without waiting until the belt is full. sender would block buffer if channel is full 
- `sync.WaitGroup` is a thread safe counter. used to wait for collection of goroutines to finish when main() finishes or dies.
- There is no `interface` "inheriting" in go. Unlike the traditional create a prototype and define your class. Go has implicit implementation, if a type has all the methods that an interface asks for, then it automatically implements that interface. 
    - if a hard check is required we can add at the end of implementation var _ Interface = (*Struct)(nil)