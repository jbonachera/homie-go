package homie

// MqttConfig broker config
type MqttConfig struct {
	URL              string
	Username         string
	Password         string
	OnConnect        func()
	OnConnectionLost func(err error)
	OnBroadcast      func(level string, message []byte)
}

// Config homie config
type Config struct {
	Mqtt                MqttConfig
	BaseTopic           string // must end with '/'
	StatsReportInterval int    // in seconds
}
