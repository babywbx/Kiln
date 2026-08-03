package staticcatalog

import (
	"github.com/babywbx/kiln/modules/channelconfig"
	"github.com/babywbx/kiln/modules/config"
)

type Service struct {
	cfg config.File
}

func New(cfg config.File) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Config() config.File {
	return s.cfg
}

func (s *Service) Get(id string) (config.Channel, bool) {
	channel, ok := s.cfg.ChannelByID(id)
	if !ok || channel.Disabled {
		return config.Channel{}, false
	}
	return channel, true
}

func (s *Service) List() []config.Channel {
	return s.cfg.ActiveChannels()
}

func (s *Service) ActiveChannels() []config.Channel {
	return s.cfg.ActiveChannels()
}

func (s *Service) FilterByIDs(channels []config.Channel, ids []string) []config.Channel {
	return channelconfig.FilterByIDs(channels, ids)
}

func (s *Service) Playlist(channels []config.Channel, token string) string {
	return channelconfig.M3U(channels, channelconfig.M3UOptions{
		PublicBase: s.cfg.Server.PublicBaseURL, PlayPathPrefix: "/v1/play/", Token: token,
	})
}

func (s *Service) SourceURL(channel config.Channel) (string, error) {
	return channelconfig.SourceURL(s.cfg, channel)
}

func (s *Service) Upstream(channel config.Channel) (config.Upstream, error) {
	return channelconfig.Upstream(s.cfg, channel)
}
