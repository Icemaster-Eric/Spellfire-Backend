package game

import (
	"math"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/Icemaster-Eric/Spellfire/backend/internal/game/archetype"
	"github.com/Icemaster-Eric/Spellfire/backend/internal/game/component"
	"github.com/Icemaster-Eric/Spellfire/backend/internal/game/entity"
	"github.com/Icemaster-Eric/Spellfire/backend/internal/pb"
	"github.com/kelindar/column"
	"google.golang.org/protobuf/proto"
)

const (
	TickRate = 20 // ticks per second
)

type Player struct {
	ID     uint32
	Inputs chan *pb.ClientPacket
	mu     sync.Mutex
	packet *pb.ServerPacket
}

func NewPlayer(id uint32) *Player {
	return &Player{
		ID:     id,
		Inputs: make(chan *pb.ClientPacket, 1),
		packet: &pb.ServerPacket{},
	}
}

func (p *Player) WriteUpdate(fn func(pkt *pb.ServerPacket)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p.packet)
}

// if needed to read current packet, returns a copy
func (p *Player) ReadUpdate() *pb.ServerPacket {
	p.mu.Lock()
	defer p.mu.Unlock()
	return proto.Clone(p.packet).(*pb.ServerPacket)
}

type World struct {
	NextEntityID uint32
	Archetypes   map[*big.Int]*archetype.Archetype
	Players      map[string]*Player
}

func NewWorld() *World {
	archetypes := make(map[*big.Int]*archetype.Archetype)
	players := make(map[string]*Player)

	return &World{
		Archetypes: archetypes,
		Players: players,
	}
}

func (w *World) GetArchetype(signature *big.Int) *archetype.Archetype {
	if a, ok := w.Archetypes[signature]; ok {
		return a
	}
	collection := column.NewCollection()
	for i := 0; i < signature.BitLen(); i++ {
		if signature.Bit(i) == 1 {
			component.ComponentMap[i](collection)
		}
	}
	newSignature := new(big.Int).Set(signature)
	a := &archetype.Archetype{
		Signature: newSignature,
		Entities:  make([]uint32, 0),
		Columns:   collection,
	}
	w.Archetypes[newSignature] = a
	return a
}

func (w *World) QueryArchetypes(withSig *big.Int, withoutSig *big.Int) (archs []*archetype.Archetype) {
	checker := archetype.NewSignatureChecker(withSig, withoutSig)

	for signature, arch := range w.Archetypes {
		if checker.MatchesWithWithout(signature) {
			archs = append(archs, arch)
		}
	}
	return archs
}

func (w *World) QueryArchetypesWith(withSig *big.Int) (archs []*archetype.Archetype) {
	return w.QueryArchetypes(withSig, big.NewInt(0))
}

func (w *World) QueryArchetypesWithout(withoutSig *big.Int) (archs []*archetype.Archetype) {
	return w.QueryArchetypes(big.NewInt(0), withoutSig)
}

func (w *World) SpawnEntity(e entity.Entity) uint32 {
	entityID := w.NextEntityID
	w.NextEntityID += 1

	a := w.GetArchetype(e.GetSignature())
	a.AddEntity(entityID, e.Insert)

	return entityID
}

func (w *World) DespawnEntity(entityID uint32, signature *big.Int) {
	a, ok := w.Archetypes[signature]
	if ok {
		a.RemoveEntity(entityID)
	}
}

func (w *World) SpawnPlayer(name string) {
	e := entity.Player{
		NAME: name,
	}
	entityID := w.SpawnEntity(e)
	p := NewPlayer(entityID)
	w.Players[name] = p
	p.WriteUpdate(func(pkt *pb.ServerPacket) {
		pkt.Events = append(pkt.Events, &pb.ServerEvent{
			Type:              pb.ServerEventType_SERVER_EVENT_TYPE_ENTER_GAME,
			EnterGamePlayerId: entityID,
		})
	})
}

func (w *World) DespawnPlayer(name string) {
	p, ok := w.Players[name]
	if ok {
		w.DespawnEntity(p.ID, entity.PlayerSignature())
	}
	delete(w.Players, name)
}

func (w *World) MovePlayer(name string, movement *pb.Vec2) {
	l := math.Hypot(movement.X, movement.Y)
	nx, ny := movement.X/l, movement.Y/l
	p, ok := w.Players[name]
	if ok {
		a := w.GetArchetype(entity.PlayerSignature())
		a.Columns.QueryKey(strconv.FormatUint(uint64(p.ID), 16), func(r column.Row) error {
			r.SetFloat64("VX", nx)
			r.SetFloat64("VY", ny)
			return nil
		})
	}
}

func (w *World) UpdatePlayers() {
	a := w.GetArchetype(entity.PlayerSignature())
	a.Columns.Query(func(txn *column.Txn) error {
		nameCol := txn.String("NAME")
		xCol := txn.Float64("X")
		yCol := txn.Float64("Y")
		vxCol := txn.Float64("VX")
		vyCol := txn.Float64("VY")
		rotationCol := txn.Float64("ROTATION")

		return txn.Range(func(idx uint32) {
			name, _ := nameCol.Get()
			x, _ := xCol.Get()
			y, _ := yCol.Get()
			vx, _ := vxCol.Get()
			vy, _ := vyCol.Get()
			rotation, _ := rotationCol.Get()
			var radius float64 = 0.5 // temporary

			for _, player := range w.Players {
				player.WriteUpdate(func(pkt *pb.ServerPacket) {
					pkt.Entities = append(pkt.Entities, &pb.Entity{
						Id:   player.ID,
						Type: pb.EntityType_ENTITY_TYPE_PLAYER_GUNNER,
						Collider: &pb.Collider{
							Type:     pb.ColliderType_COLLIDER_TYPE_CIRCLE,
							Rotation: rotation,
							Radius:   radius,
							Position: &pb.Vec2{X: x, Y: y},
							Velocity: &pb.Vec2{X: vx, Y: vy},
						},
						RenderData: &pb.RenderData{
							Sprite: pb.Sprite_SPRITE_PLAYER_GUNNER,
						},
						Attributes: []*pb.EntityAttribute{
							{
								Type: pb.EntityAttributeType_ENTITY_ATTRIBUTE_TYPE_NAME,
								Name: name,
							},
						},
					})
				})
			}
		})
	})
	// add timestamps to each packet
	for _, player := range w.Players {
		player.WriteUpdate(func(pkt *pb.ServerPacket) {
			pkt.Timestamp = &pb.Timestamp{Ms:uint64(time.Millisecond)}
		})
	}
}

func (w *World) Tick(dt float64) {
	// Consume player inputs
	for name, player := range w.Players {
		for range len(player.Inputs) {
			packet := <-player.Inputs
			for _, event := range packet.Events {
				switch event.Type {
				case pb.ClientEventType_MOVE:
					w.MovePlayer(name, event.Movement)
				}
			}
		}
	}
	// Systems
	w.MoveEntities(dt)
	// Send updates
	w.UpdatePlayers()
}
