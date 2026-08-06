package modules

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/custombot/bot/logger"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"gopkg.in/hraban/opus.v2"
)

// VoiceManager manages Discord voice connections and audio playback for the bot.
// It wraps disgo's voice.Manager and provides a simplified API for joining/leaving
// voice channels and playing audio via FFmpeg.
type VoiceManager struct {
	vm     voice.Manager
	log    *logger.Logger
	guilds map[snowflake.ID]*guildVoice
	mu     sync.RWMutex
}

type guildVoice struct {
	conn      voice.Conn
	channelID snowflake.ID
	cancel    context.CancelFunc
	provider  *ffmpegProvider
	paused    bool
	volume    float64
	gvMu      sync.Mutex
}

// NewVoiceManager creates a new VoiceManager wrapping disgo's voice.Manager.
func NewVoiceManager(vm voice.Manager, log *logger.Logger) *VoiceManager {
	return &VoiceManager{
		vm:     vm,
		log:    log,
		guilds: make(map[snowflake.ID]*guildVoice),
	}
}

// JoinVoice connects the bot to a voice channel in a guild.
// Uses double-check locking to prevent TOCTOU races where two goroutines
// create voice connections for the same guild concurrently.
func (m *VoiceManager) JoinVoice(guildID, channelID string) error {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}
	cID, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("invalid channel ID: %w", err)
	}

	m.mu.Lock()
	if existing, ok := m.guilds[gID]; ok {
		if existing.channelID == cID {
			m.mu.Unlock()
			return nil // already in this channel
		}
		// Leave old channel — release lock since leaveLocked must not hold it
		m.mu.Unlock()
		_ = m.leaveLocked(gID, existing)
	} else {
		m.mu.Unlock()
	}

	// Create a new voice connection (no lock held — Open may block)
	conn := m.vm.CreateConn(gID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := conn.Open(ctx, cID, false, false); err != nil {
		return fmt.Errorf("voice connect failed: %w", err)
	}

	gv := &guildVoice{
		conn:      conn,
		channelID: cID,
		volume:    1.0,
	}

	// Re-acquire lock and double-check for concurrent joins
	m.mu.Lock()
	if _, ok := m.guilds[gID]; ok {
		// Another goroutine connected while we were opening — close ours
		conn.Close(context.Background())
		m.mu.Unlock()
		return fmt.Errorf("another voice connection was created for guild %s", guildID)
	}
	m.guilds[gID] = gv
	m.mu.Unlock()

	m.log.Info("Joined voice channel %s in guild %s", channelID, guildID)
	return nil
}

// LeaveVoice disconnects from the voice channel in a guild.
func (m *VoiceManager) LeaveVoice(guildID string) error {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}

	m.mu.Lock()
	gv, ok := m.guilds[gID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("not connected to voice in guild %s", guildID)
	}
	delete(m.guilds, gID)
	m.mu.Unlock()

	return m.leaveLocked(gID, gv)
}

func (m *VoiceManager) leaveLocked(gID snowflake.ID, gv *guildVoice) error {
	if gv.cancel != nil {
		gv.cancel()
	}
	gv.conn.Close(context.Background())
	m.vm.RemoveConn(gID)
	m.log.Info("Left voice channel in guild %s", gID.String())
	return nil
}

// PlayAudio starts playing audio from a source (URL/file) in the guild's voice channel.
func (m *VoiceManager) PlayAudio(guildID, source string) error {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}

	m.mu.RLock()
	gv, ok := m.guilds[gID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("not connected to voice in guild %s", guildID)
	}

	gv.gvMu.Lock()
	defer gv.gvMu.Unlock()

	// Stop any current playback
	if gv.cancel != nil {
		gv.cancel()
		time.Sleep(100 * time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	gv.cancel = cancel
	gv.paused = false

	provider, err := newFFmpegProvider(ctx, source, gv.volume, m.log)
	if err != nil {
		cancel()
		gv.cancel = nil
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}
	gv.provider = provider

	gv.conn.SetOpusFrameProvider(provider)
	m.log.Info("Playing audio in guild %s: %s", guildID, source)
	return nil
}

// StopAudio stops current audio playback without disconnecting.
func (m *VoiceManager) StopAudio(guildID string) error {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}

	m.mu.RLock()
	gv, ok := m.guilds[gID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("not connected to voice in guild %s", guildID)
	}

	gv.gvMu.Lock()
	defer gv.gvMu.Unlock()

	if gv.cancel != nil {
		gv.cancel()
		gv.cancel = nil
	}
	gv.provider = nil

	// Clear the provider to stop playback but stay connected
	gv.conn.SetOpusFrameProvider(nil)
	m.log.Info("Stopped audio in guild %s", guildID)
	return nil
}

// PauseAudio pauses current playback.
func (m *VoiceManager) PauseAudio(guildID string) error {
	return m.setPause(guildID, true)
}

// ResumeAudio resumes paused playback.
func (m *VoiceManager) ResumeAudio(guildID string) error {
	return m.setPause(guildID, false)
}

func (m *VoiceManager) setPause(guildID string, pause bool) error {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}

	m.mu.RLock()
	gv, ok := m.guilds[gID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("not connected to voice in guild %s", guildID)
	}

	gv.gvMu.Lock()
	defer gv.gvMu.Unlock()

	if gv.provider == nil {
		return fmt.Errorf("no active playback")
	}

	gv.paused = pause
	gv.provider.setPaused(pause)
	return nil
}

// SetVolume changes the playback volume (0.0 to 2.0). 1.0 is normal.
func (m *VoiceManager) SetVolume(guildID string, volume float64) error {
	if volume < 0 || volume > 2.0 {
		return fmt.Errorf("volume must be between 0.0 and 2.0")
	}

	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}

	m.mu.RLock()
	gv, ok := m.guilds[gID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("not connected to voice in guild %s", guildID)
	}

	gv.gvMu.Lock()
	gv.volume = volume
	if gv.provider != nil {
		gv.provider.setVolume(volume)
	}
	gv.gvMu.Unlock()

	return nil
}

// SetMute toggles self-mute on the voice connection.
// NOTE: Self-mute requires a Discord gateway voice state update (opcode 4),
// not a speaking indicator. This is not yet implemented via the REST gateway.
func (m *VoiceManager) SetMute(guildID string, mute bool) error {
	_, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}
	return fmt.Errorf("SetMute is not implemented — self-mute requires a gateway voice state update")
}

// SetDeafen toggles self-deafen on the voice connection.
// NOTE: Self-deafen requires a Discord gateway voice state update (opcode 4).
// This is not yet implemented via the REST gateway.
func (m *VoiceManager) SetDeafen(guildID string, deafen bool) error {
	_, err := snowflake.Parse(guildID)
	if err != nil {
		return fmt.Errorf("invalid guild ID: %w", err)
	}
	return fmt.Errorf("SetDeafen is not implemented — self-deafen requires a gateway voice state update")
}

// IsConnected returns true if the bot is connected to a voice channel in the guild.
func (m *VoiceManager) IsConnected(guildID string) bool {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return false
	}
	m.mu.RLock()
	_, ok := m.guilds[gID]
	m.mu.RUnlock()
	return ok
}

// IsPlaying returns true if audio is currently playing in the guild.
func (m *VoiceManager) IsPlaying(guildID string) bool {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return false
	}
	m.mu.RLock()
	gv, ok := m.guilds[gID]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	gv.gvMu.Lock()
	playing := gv.provider != nil && !gv.paused
	gv.gvMu.Unlock()
	return playing
}

// Close disconnects all voice connections and shuts down the manager.
func (m *VoiceManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for gID, gv := range m.guilds {
		if gv.cancel != nil {
			gv.cancel()
		}
		gv.conn.Close(context.Background())
		m.vm.RemoveConn(gID)
	}
	m.guilds = make(map[snowflake.ID]*guildVoice)
}

// ── FFmpeg Audio Provider ──────────────────────────────────────────

// ffmpegProvider implements voice.OpusFrameProvider by streaming audio from FFmpeg.
// It decodes audio via FFmpeg to PCM and encodes to Opus for Discord.
type ffmpegProvider struct {
	ctx     context.Context
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	enc     *opus.Encoder
	volume  float64
	paused  bool
	pauseCh chan struct{}
	mu      sync.Mutex
	log     *logger.Logger
}

func newFFmpegProvider(ctx context.Context, source string, volume float64, log *logger.Logger) (*ffmpegProvider, error) {
	p := &ffmpegProvider{
		ctx:     ctx,
		volume:  volume,
		pauseCh: make(chan struct{}),
		log:     log,
	}
	if err := p.startFFmpeg(source); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *ffmpegProvider) startFFmpeg(source string) error {
	// Check if FFmpeg is available
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found: install ffmpeg to play audio")
	}

	// Spawn FFmpeg: decode source to PCM s16le 48kHz stereo
	cmd := exec.CommandContext(p.ctx, "ffmpeg",
		"-v", "quiet",
		"-i", source,
		"-f", "s16le",
		"-ar", "48000",
		"-ac", "2",
		"pipe:1",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Capture stderr for error logging
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 1024), 1024)
		for scanner.Scan() {
			p.log.Warn("FFmpeg: %s", scanner.Text())
		}
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start failed: %w", err)
	}

	p.cmd = cmd
	p.stdout = stdout

	// Create Opus encoder (48kHz, stereo, audio optimized)
	enc, err := opus.NewEncoder(48000, 2, opus.AppAudio)
	if err != nil {
		p.cmd.Process.Kill()
		return fmt.Errorf("failed to create Opus encoder: %w", err)
	}
	p.enc = enc

	return nil
}

// ProvideOpusFrame returns the next Opus-encoded audio frame.
func (p *ffmpegProvider) ProvideOpusFrame() ([]byte, error) {
	// Check pause state
	p.mu.Lock()
	for p.paused {
		pauseCh := p.pauseCh
		p.mu.Unlock()
		select {
		case <-pauseCh:
		case <-p.ctx.Done():
			return nil, io.EOF
		}
		p.mu.Lock()
	}

	vol := p.volume
	p.mu.Unlock()

	// Ensure FFmpeg is running
	if p.cmd == nil {
		return nil, io.EOF
	}

	// Read 20ms of PCM: 960 samples per channel * 2 channels = 1920 int16 = 3840 bytes
	pcmBuf := make([]int16, 960*2)
	if err := binary.Read(p.stdout, binary.LittleEndian, &pcmBuf); err != nil {
		p.log.Debug("FFmpeg stream ended: %v", err)
		p.cleanupFFmpeg()
		return nil, io.EOF
	}

	// Apply volume
	if vol != 1.0 {
		for i := range pcmBuf {
			s := float64(pcmBuf[i]) * vol
			if s > 32767 {
				s = 32767
			} else if s < -32768 {
				s = -32768
			}
			pcmBuf[i] = int16(s)
		}
	}

	// Encode PCM to Opus
	opusBuf := make([]byte, 4000) // max Opus frame size
	n, err := p.enc.Encode(pcmBuf, opusBuf)
	if err != nil {
		p.log.Error("Opus encode error: %v", err)
		p.cleanupFFmpeg()
		return nil, io.EOF
	}

	return opusBuf[:n], nil
}

// Close implements OpusFrameProvider.Close.
func (p *ffmpegProvider) Close() {
	p.cleanupFFmpeg()
}

func (p *ffmpegProvider) cleanupFFmpeg() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	p.cmd = nil
	p.stdout = nil
}

func (p *ffmpegProvider) setPaused(paused bool) {
	p.mu.Lock()
	p.paused = paused
	if !paused {
		close(p.pauseCh)
		p.pauseCh = make(chan struct{})
	}
	p.mu.Unlock()
}

func (p *ffmpegProvider) setVolume(volume float64) {
	p.mu.Lock()
	p.volume = volume
	p.mu.Unlock()
}
