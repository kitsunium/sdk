package multi

// Queued is the count of commands waiting for the next entry.
func Queued(p *Packer) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue)
}

// Body is the packed body of cmds.
func Body(cmds ...string) []byte {
	bs := make([][]byte, len(cmds))
	for i, c := range cmds {
		bs[i] = []byte(c)
	}
	return appendBody(nil, bs)
}
