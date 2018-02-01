package clientserver

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/centrifugal/centrifugo/lib/client"
	"github.com/centrifugal/centrifugo/lib/logger"
	"github.com/centrifugal/centrifugo/lib/node"
	"github.com/centrifugal/centrifugo/lib/proto"

	"google.golang.org/grpc/metadata"
)

// Config for GRPC API server.
type Config struct{}

// Server can answer on GRPC API requests.
type Server struct {
	config Config
	node   *node.Node
}

// New creates new server.
func New(n *node.Node, c Config) *Server {
	return &Server{
		config: c,
		node:   n,
	}
}

// Communicate ...
func (s *Server) Communicate(stream proto.Centrifugo_CommunicateServer) error {

	replies := make(chan *proto.Reply, 64)
	transport := newGRPCTransport(stream, replies)

	c := client.New(stream.Context(), s.node, transport, client.Config{})
	defer c.Close(proto.DisconnectNormal)

	go func() {
		for {
			cmd, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				return
			}
			rep, disconnect := c.Handle(cmd)
			if disconnect != nil {
				logger.ERROR.Printf("disconnect after handling command %v: %v", cmd, disconnect)
				transport.Close(disconnect)
				return
			}
			transport.Send(proto.NewPreparedReply(rep, proto.EncodingProtobuf))
		}
	}()

	for reply := range replies {
		if err := stream.Send(reply); err != nil {
			return err
		}
	}

	return nil
}

// grpcTransport ...
type grpcTransport struct {
	mu      sync.Mutex
	closed  bool
	stream  proto.Centrifugo_CommunicateServer
	replies chan *proto.Reply
}

func newGRPCTransport(stream proto.Centrifugo_CommunicateServer, replies chan *proto.Reply) *grpcTransport {
	return &grpcTransport{
		stream:  stream,
		replies: replies,
	}
}

func (t *grpcTransport) Name() string {
	return "grpc"
}

func (t *grpcTransport) Send(reply *proto.PreparedReply) error {
	select {
	case t.replies <- reply.Reply:
	default:
		return fmt.Errorf("error sending to transport: buffer channel is full")
	}
	return nil
}

func (t *grpcTransport) Close(disconnect *proto.Disconnect) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	disconnectJSON, err := json.Marshal(disconnect)
	if err != nil {
		return err
	}
	t.stream.SetTrailer(metadata.Pairs("disconnect", string(disconnectJSON)))
	close(t.replies)
	return nil
}
