- `net/http` inbuilt package used for spinning up http server
- := is used to declare & initialize (short hand declaration)
    - `name := "John"` this would assign and infer type as string
    - `var name string` `name = "John"` would work
    - but `name = "John"` would not work because type cant be inferred
    - `:=` can only be used inside functions (not at package level)
- `log.Fatalf` is used to log a fatal error.
- Has Swift's `if let` binding, which can be used as if with a short variable declaration. `if err := json.NewEncoder(w).Encode(resp); err != nil`
- `func (s *Server) handleCreateTopic(w http.ResponseWriter, r *http.Request)` here `(s *Server)` specifies that the `handleCreateTopic` function can be called only on instances of Server struct. Go's way for class methods.
    - The `s` is the receiver name (like `self` in Python or `this` in JavaScript)
    - `*Server` means it's a pointer receiver - the method can modify the struct and avoids copying
    - Could also be value receiver `(s Server)` but pointer is preferred for structs to avoid copying overhead
    - Methods with pointer receivers can be called on both pointers and values (Go automatically handles the conversion)
    - This allows you to call `server.handleCreateTopic()` where `server` is an instance of `Server`
- `package {folder name}` all files in a folder should start with package then foldername to denote that those files are part of the package and can be imported as an entire package.
- Go heavily relies on pointer memory access for references using `*` and `&`:
    - `&` is the address-of operator - gets the memory address of a variable
    - `*` is the dereference operator - accesses the value at a memory address
    - Example: `var x int = 42; ptr := &x; value := *ptr` 
    - Function parameters are passed by value by default, so use pointers to modify original values
    - Struct methods often use pointer receivers `(s *Server)` to avoid copying large structs and allow modifications
    - Interfaces are automatically handled as pointers when implemented by pointer receivers
    - Unlike C/C++, Go has garbage collection so manual memory management isn't needed
- String connontatiosn
    - `%s` string
    - `%d` base-10 decimal
    - `%v` formats in natural way? for structs prints the values but not field names
    - `%+v` includes field names
    - `%w` used onl with `fmt.Errorf`
- No `try...catch` error handling is more explicit forcing user to handle error and then move on as errors are presented as regular return values.
- `go.mod` - package.json. `go.sum` - package-lock.json

