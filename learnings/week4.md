#Channels
- a go channel is a pipe or conveyor belt with 2 types 
    - unbuffered `make(chan Job)` - synchronous hand-off. sender blocks until receiver takes the item. if no is ready to receive, sender waits forever.
    - buffered `make(chan Job, 100)` - a queue with a specific capacity (100). sender will drop items without waiting until the belt is full. sender would block buffer if channel is full 