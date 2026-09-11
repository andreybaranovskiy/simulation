package interp

import (
	"container/heap"
	"fmt"
	"math"

	"github.com/andreybaranovskiy/simulation/internal/engine/spec"
)

// network is the travel graph laid over the site plan. Distances come from the
// calibrated node coordinates, so a travel time is a real distance divided by
// a real speed rather than an invented number.
type network struct {
	nodes   []spec.Node
	indexOf map[string]int
	edges   [][]edge
	// paths caches shortest routes between node pairs. Entities repeat the
	// same journeys constantly, so computing each one once matters.
	paths map[pathKey][]int
	// freeMovement is true when the model declares no links at all, in which
	// case entities travel in a straight line between any two nodes.
	freeMovement bool
}

type edge struct {
	to       int
	distance float64
	// speedLimit of zero means the entity's own speed applies.
	speedLimit float64
	// link points back at the spec entry, so a capacity-limited link can be
	// held as a resource during traversal.
	link *spec.Link
}

type pathKey struct{ from, to int }

func buildNetwork(m *spec.Model) (*network, error) {
	n := &network{
		nodes:        m.Nodes,
		indexOf:      make(map[string]int, len(m.Nodes)),
		edges:        make([][]edge, len(m.Nodes)),
		paths:        make(map[pathKey][]int),
		freeMovement: len(m.Links) == 0,
	}

	for i, node := range m.Nodes {
		n.indexOf[node.ID] = i
	}

	for i := range m.Links {
		l := &m.Links[i]

		from, ok := n.indexOf[l.From]
		if !ok {
			return nil, fmt.Errorf("link refers to unknown node %q", l.From)
		}
		to, ok := n.indexOf[l.To]
		if !ok {
			return nil, fmt.Errorf("link refers to unknown node %q", l.To)
		}

		distance := l.Distance
		if distance <= 0 {
			distance = n.straightLine(from, to)
		}

		n.edges[from] = append(n.edges[from], edge{to: to, distance: distance, speedLimit: l.SpeedLimit, link: l})
		if l.Bidirectional {
			n.edges[to] = append(n.edges[to], edge{to: from, distance: distance, speedLimit: l.SpeedLimit, link: l})
		}
	}

	return n, nil
}

func (n *network) index(id string) (int, bool) {
	i, ok := n.indexOf[id]
	return i, ok
}

func (n *network) position(i int) (x, y, z float64) {
	node := n.nodes[i]
	return node.X, node.Y, node.Z
}

func (n *network) straightLine(a, b int) float64 {
	na, nb := n.nodes[a], n.nodes[b]
	dx, dy, dz := nb.X-na.X, nb.Y-na.Y, nb.Z-na.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// leg is one hop of a journey.
type leg struct {
	to         int
	distance   float64
	speedLimit float64
	link       *spec.Link
}

// route returns the legs from one node to another, or nil when unreachable.
//
// With no links declared the model is treated as open ground and the journey
// is a single straight leg. That keeps simple models simple: a user sketching
// three nodes should not have to draw the roads between them.
func (n *network) route(from, to int) []leg {
	if from == to {
		return nil
	}
	if n.freeMovement {
		return []leg{{to: to, distance: n.straightLine(from, to)}}
	}

	key := pathKey{from, to}
	nodePath, cached := n.paths[key]
	if !cached {
		nodePath = n.dijkstra(from, to)
		n.paths[key] = nodePath
	}
	if len(nodePath) < 2 {
		return nil
	}

	legs := make([]leg, 0, len(nodePath)-1)
	for i := 0; i+1 < len(nodePath); i++ {
		e := n.edgeBetween(nodePath[i], nodePath[i+1])
		if e == nil {
			return nil
		}
		legs = append(legs, leg{to: e.to, distance: e.distance, speedLimit: e.speedLimit, link: e.link})
	}
	return legs
}

func (n *network) edgeBetween(from, to int) *edge {
	best := (*edge)(nil)
	for i := range n.edges[from] {
		e := &n.edges[from][i]
		if e.to != to {
			continue
		}
		if best == nil || e.distance < best.distance {
			best = e
		}
	}
	return best
}

// dijkstra finds the shortest node path by distance. Travel time would be the
// more faithful weight, but speed varies per entity, and a path that changed
// with the traveller could not be cached. Distance is stable and, with speed
// limits that rarely differ by much, gives the same answer nearly always.
func (n *network) dijkstra(from, to int) []int {
	const unreachable = math.MaxFloat64

	dist := make([]float64, len(n.nodes))
	prev := make([]int, len(n.nodes))
	for i := range dist {
		dist[i] = unreachable
		prev[i] = -1
	}
	dist[from] = 0

	pq := &nodeQueue{{node: from, cost: 0}}
	heap.Init(pq)
	visited := make([]bool, len(n.nodes))

	for pq.Len() > 0 {
		item := heap.Pop(pq).(queueItem)
		if visited[item.node] {
			continue
		}
		visited[item.node] = true

		if item.node == to {
			break
		}

		for _, e := range n.edges[item.node] {
			if visited[e.to] {
				continue
			}
			next := dist[item.node] + e.distance
			if next < dist[e.to] {
				dist[e.to] = next
				prev[e.to] = item.node
				heap.Push(pq, queueItem{node: e.to, cost: next})
			}
		}
	}

	if dist[to] == unreachable {
		return nil
	}

	// Walk the predecessors back and reverse.
	path := []int{to}
	for at := to; prev[at] != -1; at = prev[at] {
		path = append(path, prev[at])
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

type queueItem struct {
	node int
	cost float64
}

type nodeQueue []queueItem

func (q nodeQueue) Len() int           { return len(q) }
func (q nodeQueue) Less(i, j int) bool { return q[i].cost < q[j].cost }
func (q nodeQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *nodeQueue) Push(x any)        { *q = append(*q, x.(queueItem)) }
func (q *nodeQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	*q = old[:n-1]
	return item
}
