package homie

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Device homie device
type Device interface {
	Name() string
	Stats() DeviceStats
	NewNode(name string, nodeType string) Node
	AddNode(node Node) Node
	GetNode(name string) Node
	Connect() error
	Run(block bool)
	Config() *Config
	Client() MqttAdapter
	OnConnect(client MqttAdapter)
	OnConnectionLost(client MqttAdapter, err error)

	// Topic returns full topic for a part, prefixed with baseTopic and deviceName
	Topic(part string) string
	SendMessage(topic string, value string)
	DevicePublisher() DevicePublisher
	SetDevicePublisher(publisher DevicePublisher) Device

	PublishStats()

	Disconnect() error
}

// DeviceStats stats about device like startup, connect time, etc
type DeviceStats interface {
	StartupTime() time.Time
	ConnectTime() time.Time
}

type device struct {
	name      string
	config    *Config
	nodes     map[string]Node
	stats     *deviceStats
	publisher DevicePublisher
	client    MqttAdapter

	mutex *sync.Mutex
}

type deviceStats struct {
	startupTime time.Time
	connectTime time.Time
}

func (s *deviceStats) StartupTime() time.Time {
	return s.startupTime
}

func (s *deviceStats) ConnectTime() time.Time {
	return s.connectTime
}

// NewDevice create new homie device
func NewDevice(name string, cfg *Config) Device {
	return &device{
		name:   name,
		config: cfg,
		stats: &deviceStats{
			startupTime: time.Now(),
		},
		mutex: &sync.Mutex{},
	}
}

func (d *device) Name() string {
	return d.name
}

func (d *device) Stats() DeviceStats {
	return d.stats
}

func (d *device) Client() MqttAdapter {
	return d.client
}

func (d *device) Config() *Config {
	return d.config
}

func (d *device) GetNode(name string) Node {
	return d.nodes[name]
}
func (d *device) NewNode(name string, nodeType string) Node {
	return d.AddNode(&node{
		name:     name,
		nodeType: nodeType,
	})
}

func (d *device) AddNode(node Node) Node {
	node.SetDevice(d)
	if d.nodes == nil {
		d.nodes = make(map[string]Node)
	}
	if _, alreadyAdded := d.nodes[node.Name()]; alreadyAdded {
		log.Panic(fmt.Errorf("Node %s already added", node.Name()))
	}
	d.nodes[node.Name()] = node
	return node
}
func (d *device) Connect() error {
	options := d.createMqttOptions()
	return d.connect(options)

}
func (d *device) Run(block bool) {
	d.Connect()

	if block {
		select {} // block forever
	}
}

func (d *device) createMqttOptions() *mqtt.ClientOptions {
	brokerURL, err := url.Parse(d.config.Mqtt.URL)
	if err != nil {
		panic(err)
	}
	tlsConfig := &tls.Config{
		ServerName: brokerURL.Hostname(),
	}
	opts := mqtt.NewClientOptions()
	opts.AddBroker(d.config.Mqtt.URL)
	opts.SetUsername(d.config.Mqtt.Username)
	opts.SetPassword(d.config.Mqtt.Password)
	opts.SetClientID(d.name)
	opts.SetBinaryWill(d.Topic("$state"), []byte("lost"), 1, true)
	opts.SetAutoReconnect(true)
	opts.SetTLSConfig(tlsConfig)
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		if d.config != nil && d.config.Mqtt.OnConnectionLost != nil {
			d.config.Mqtt.OnConnectionLost(d, err)
		}
		d.OnConnectionLost(&mqttClientDelegate{
			client: c,
		}, err)
	})
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		// TODO: refactor this, currently it creates multiple instances of delegates on re-connect
		d.OnConnect(&mqttClientDelegate{
			client: c,
		})
		if d.config != nil && d.config.Mqtt.OnConnect != nil {
			d.config.Mqtt.OnConnect(d)
		}
	})
	return opts
}

func (d *device) OnConnect(client MqttAdapter) {
	d.client = client
	d.stats.connectTime = time.Now()
	d.initNodes()
	d.initDevice()
}
func (d *device) OnConnectionLost(client MqttAdapter, err error) {
}

func (d *device) connect(options *mqtt.ClientOptions) error {
	client := mqtt.NewClient(options)
	token := client.Connect() // start connecting to broker, initialisation is done in onConnectHandler
	for !token.WaitTimeout(3 * time.Second) {
	}
	if err := token.Error(); err != nil {
		return err
	}
	return nil
}

func (d *device) Topic(part string) string {
	return fmt.Sprintf("%s%s/%s", d.config.BaseTopic, d.Name(), part)
}

func (d *device) SendMessage(topic string, message string) {
	d.client.Publish(d.Topic(topic), 1, true, message)
}

func (d *device) DevicePublisher() DevicePublisher {
	return d.publisher
}

func (d *device) SetDevicePublisher(publisher DevicePublisher) Device {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	if d.publisher != nil {
		panic(errors.New("DevicePublisher is already configured"))
	}
	d.publisher = publisher
	return d
}

func (d *device) PublishStats() {
	diff := time.Since(d.Stats().StartupTime())
	d.SendMessage("$stats/uptime", fmt.Sprintf("%d", uint64(diff.Seconds())))
}

func (d *device) initDevice() {
	if !d.client.IsConnected() {
		panic("not connected")
	}
	d.SendMessage("$homie", HomieSpecVersion)
	d.SendMessage("$name", d.name)
	d.SendMessage("$localip", outboundIP())
	d.SendMessage("$implementation", "homie-go")
	d.SendMessage("$state", "ready")
	d.SendMessage("$stats/interval", fmt.Sprintf("%d", d.config.StatsReportInterval))

	var nodeNames []string
	for _, n := range d.nodes {
		nodeNames = append(nodeNames, n.Name())
	}
	d.SendMessage("$nodes", strings.Join(nodeNames, ","))
	for _, n := range d.nodes {
		n.Publish()
	}

	if d.publisher != nil {
		d.publisher(d)
	}
	d.PublishStats()
	d.client.Subscribe(fmt.Sprintf("%s$broadcast/+", d.config.BaseTopic), 1, func(_ mqtt.Client, message mqtt.Message) {
		if d.config.Mqtt.OnBroadcast != nil {
			d.config.Mqtt.OnBroadcast(d, strings.TrimPrefix(message.Topic(), fmt.Sprintf("%s$broadcast/", d.config.BaseTopic)), message.Payload())
		}
	})
}

func (d *device) initNodes() {
	for _, n := range d.nodes {
		n.Subscribe()
		if n.NodePublisher() != nil {
			n.NodePublisher()(n) // invoke publishers
		}
	}
}

func (d *device) Disconnect() error {
	d.SendMessage("$state", "disconnected")
	d.client.Disconnect(500)
	return nil
}
