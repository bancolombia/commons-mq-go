//go:build integration

package integration_test

// T023: Send() places message on default output queue and returns message ID.
// T024: SendTo() sends to explicitly named queue (not default).
// T025: MessageCreator callback modifications are present on the received message.
// T026: Positive ProducerTTL causes message to expire before consumption.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bancolombia/commons-mq-go/mq"
	ibmmq "github.com/ibm-messaging/mq-golang/v5/ibmmq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getMessage retrieves one message from the named queue using a direct IBM MQ
// connection, blocking for up to waitMs milliseconds. Returns nil if the queue is
// empty within the wait interval.
func getMessage(t *testing.T, cfg mq.Config, queueName string, waitMs int32) *mq.Message {
	t.Helper()

	mqcd := ibmmq.NewMQCD()
	mqcd.ChannelName = cfg.ChannelName
	mqcd.ConnectionName = cfg.ConnectionName

	mqcno := ibmmq.NewMQCNO()
	mqcno.Options = ibmmq.MQCNO_CLIENT_BINDING
	mqcno.ClientConn = mqcd

	if cfg.Username != "" {
		mqcsp := ibmmq.NewMQCSP()
		mqcsp.AuthenticationType = ibmmq.MQCSP_AUTH_USER_ID_AND_PWD
		mqcsp.UserId = cfg.Username
		mqcsp.Password = cfg.Password
		mqcno.SecurityParms = mqcsp
	}

	qmgr, err := ibmmq.Connx(cfg.QueueManagerName, mqcno)
	require.NoError(t, err, "getMessage: connect")
	defer qmgr.Disc()

	mqod := ibmmq.NewMQOD()
	mqod.ObjectName = queueName
	qObj, err := qmgr.Open(mqod, ibmmq.MQOO_INPUT_AS_Q_DEF|ibmmq.MQOO_FAIL_IF_QUIESCING)
	require.NoError(t, err, "getMessage: open queue")
	defer qObj.Close(0)

	mqmd := ibmmq.NewMQMD()
	mqgmo := ibmmq.NewMQGMO()
	mqgmo.Options = ibmmq.MQGMO_WAIT | ibmmq.MQGMO_NO_SYNCPOINT
	mqgmo.MatchOptions = 0
	mqgmo.WaitInterval = waitMs

	buf := make([]byte, 4*1024*1024)
	data, _, err := qObj.GetSlice(mqmd, mqgmo, buf)
	if mq.IsEmptyQueueError(err) {
		return nil
	}
	require.NoError(t, err, "getMessage: GetSlice")

	bodyClone := make([]byte, len(data))
	copy(bodyClone, data)
	return &mq.Message{
		ID:            fmt.Sprintf("%x", mqmd.MsgId),
		CorrelationID: fmt.Sprintf("%x", mqmd.CorrelId),
		ReplyToQueue:  mqmd.ReplyToQ,
		Body:          bodyClone,
	}
}

// T023: Send() places message on default output queue and returns message ID.
func TestSender_Integration_Send_DefaultQueue_ReturnsMessageID(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)
	cfg.OutputQueue = mqTestQueue

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	s := client.NewSender("") // empty → uses Config.OutputQueue
	msgID, err := s.Send(context.Background(), "hello-default-queue")
	require.NoError(t, err, "Send must not return an error")
	assert.NotEmpty(t, msgID, "Send must return a non-empty message ID")

	// Verify the message arrived on the queue.
	msg := getMessage(t, cfg, mqTestQueue, 3000)
	require.NotNil(t, msg, "message must appear on the queue after Send")
	assert.Equal(t, "hello-default-queue", string(msg.Body))
	assert.Equal(t, msgID, msg.ID, "returned message ID must match MQMD.MsgId on the queue")
}

// T024: SendTo() sends to an explicitly named queue, NOT the default output queue.
func TestSender_Integration_SendTo_ExplicitQueue(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)
	cfg.OutputQueue = mqTestQueue // default output

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	// Send to DEV.QUEUE.2 which is NOT the default.
	s := client.NewSender("")
	msgID, err := s.SendTo(context.Background(), mqTestQueue2, "hello-explicit-queue")
	require.NoError(t, err, "SendTo must not return an error")
	assert.NotEmpty(t, msgID)

	// The message must appear on DEV.QUEUE.2, not on DEV.QUEUE.1.
	msgOnDefault := getMessage(t, cfg, mqTestQueue, 500)
	assert.Nil(t, msgOnDefault, "message must NOT appear on the default output queue")

	msgOnExplicit := getMessage(t, cfg, mqTestQueue2, 3000)
	require.NotNil(t, msgOnExplicit, "message must appear on the explicit destination queue")
	assert.Equal(t, "hello-explicit-queue", string(msgOnExplicit.Body))
}

// T025: MessageCreator callback modifications (headers, correlation ID) are
// present on the received message.
func TestSender_Integration_MessageCreator_ModificationsApplied(t *testing.T) {
	host, port := startMQContainer(t)
	cfg := mqConfig(host, port)

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	const correlID = "0102030405060708090a0b0c0d0e0f101112131415161718" // 24-byte hex
	const replyTo = "DEV.QUEUE.3"

	s := client.NewSender(mqTestQueue)
	msgID, err := s.SendWithCreator(context.Background(), "", func(msg *mq.Message) error {
		msg.Body = []byte("creator-modified-message")
		msg.CorrelationID = correlID
		msg.ReplyToQueue = replyTo
		return nil
	})
	require.NoError(t, err, "SendWithCreator must not return an error")
	assert.NotEmpty(t, msgID)

	// Retrieve and verify the message fields.
	msg := getMessage(t, cfg, mqTestQueue, 3000)
	require.NotNil(t, msg, "message must appear on the queue")
	assert.Equal(t, "creator-modified-message", string(msg.Body))
	assert.Equal(t, correlID, msg.CorrelationID,
		"CorrelationID set by MessageCreator must be present on the received message")
	assert.Equal(t, replyTo, msg.ReplyToQueue,
		"ReplyToQueue set by MessageCreator must be present on the received message")
}

// T026: A positive ProducerTTL causes the message to expire before consumption.
func TestSender_Integration_ProducerTTL_MessageExpires(t *testing.T) {
	host, port := startMQContainer(t)

	// Very short TTL: 500ms
	cfg := mqConfig(host, port)
	cfg.ProducerTTL = 500 * time.Millisecond

	client, err := mq.NewClient(cfg)
	require.NoError(t, err)
	defer client.Close()

	s := client.NewSender(mqTestQueue3)
	_, err = s.Send(context.Background(), "ttl-expiry-test")
	require.NoError(t, err, "Send must succeed")

	// Wait longer than the TTL so the message expires.
	time.Sleep(1500 * time.Millisecond)

	// The queue must now be empty (message expired).
	msg := getMessage(t, cfg, mqTestQueue3, 500)
	assert.Nil(t, msg, "message must have expired and not be available on the queue")
}
