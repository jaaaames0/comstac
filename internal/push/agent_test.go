package push

import "testing"

func TestAgentConnectionsAreBoundedAndReleased(t *testing.T) {
	clients := NewAgentClients("0123456789abcdef0123456789abcdef")
	conns := make([]chan AgentEvent, 0, maxAgentConnections)
	for i := 0; i < maxAgentConnections; i++ {
		conn, ok := clients.Add()
		if !ok {
			t.Fatalf("connection %d rejected before limit", i+1)
		}
		conns = append(conns, conn)
	}
	if conn, ok := clients.Add(); ok || conn != nil {
		t.Fatal("connection accepted past limit")
	}

	clients.Remove(conns[0])
	conn, ok := clients.Add()
	if !ok {
		t.Fatal("released slot was not reusable")
	}
	clients.Remove(conn)
	for _, existing := range conns[1:] {
		clients.Remove(existing)
	}
}
