let ws = null;
let currentUser = null;
let currentChannel = '';
let isPTTActive = false;

// Audio system variables
let audioContext = null;
let mediaStream = null;
let nextPlayTime = 0;
let activeAudioSources = new Map(); // Track audio sources by user ID
let serverTsBaseUs = null; // Base server timestamp (microseconds) to keep JS numbers small
let audioBuffer = []; // Buffer for smooth playback
let isBuffering = false;
let isPlayingBuffer = false; // Track if we're currently playing buffered audio
let audioMixerGain = null;
let microphonePermission = false;

// WebCodecs Opus system variables
let webCodecsEncoder = null;
let webCodecsDecoder = null;
let webCodecsDecoderChannels = 1;
const OPUS_SAMPLE_RATE = 48000;
const OPUS_BITRATE = 32000;
// JS-side debug toggle: set localStorage.WT_DEBUG_AUDIO = '1' to enable verbose logs
const JS_DEBUG_AUDIO = (typeof localStorage !== 'undefined' && localStorage.getItem && localStorage.getItem('WT_DEBUG_AUDIO') === '1');

// Audio buffer configuration constants
// These values balance latency vs stability for real-time PTT communication:
// - Lower values = lower latency but higher chance of audio dropouts  
// - Higher values = more stable playback but increased latency
const FRAME_DURATION_MS = 20;                                                      // Each Opus frame is 20ms
const BUFFER_TARGET_LATENCY_MS = 60;                                              // Target buffer latency (ms)
const BUFFER_TARGET_SIZE = Math.round(BUFFER_TARGET_LATENCY_MS / FRAME_DURATION_MS); // Number of frames to buffer for target latency
const BUFFER_MAX_SIZE = BUFFER_TARGET_SIZE * 3;                                   // Maximum buffer size (3x target for network issue resilience)

// Audio validation constants
const MIN_RECEPTION_DURATION = 0.1; // Minimum duration (seconds) for valid audio reception data

// Logging configuration
const AUDIO_CHUNK_LOG_SAMPLE_RATE = 0.05; // Sample rate for audio chunk logging (5% to reduce noise)
let webCodecsSupported = false;
// Recent sequence numbers seen from server frames (simple dedupe, bounded)
const recentSeqs = new Map(); // seq -> timestamp (performance.now())
const RECENT_SEQS_MAX = 256;
// Per-user timestamp fallbacks when server timestamps are absent or not progressing
const lastServerTsByUser = new Map(); // userID -> last server tsNumber
const localTsByUser = new Map(); // userID -> local microsecond timestamp counter

// Helper function to update DOM elements safely
function updateElementText(elementId, text) {
    const element = document.getElementById(elementId);
    if (element) {
        element.textContent = text;
    }
}

// Helper function to escape HTML to prevent XSS attacks
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Helper function to create and configure an AudioDecoder
function createAudioDecoder(onOutput, onError, numberOfChannels = 1) {
    const decoder = new AudioDecoder({
        output: onOutput,
        error: onError
    });

    // Configure decoder for Opus. For multi-channel Opus some browsers require
    // an Opus identification header in `description` per WebCodecs spec.
    const cfg = {
        codec: 'opus',
        sampleRate: OPUS_SAMPLE_RATE
    };
        // Always include numberOfChannels; some environments expect it even when
        // a description OpusHead is provided.
        cfg.numberOfChannels = numberOfChannels;

        // Attempt to configure the decoder. For multi-channel streams we attach
        // an OpusHead `description` buffer; some browsers require this. If
        // configure fails we fall back to a simpler config that omits description
        // but keeps numberOfChannels so decoding may still work.
        try {
            decoder.configure(cfg);
        } catch (e) {
            const warnMsg = 'AudioDecoder.configure with OpusHead description failed, retrying without description; falling back to simpler config.';
            console.warn(warnMsg, e);
            try { addMessage('⚠️ ' + warnMsg); } catch (ignore) {}
            // Remove description and retry
            if (cfg.description) delete cfg.description;
            try {
                decoder.configure(cfg);
            } catch (e2) {
                console.error('AudioDecoder.configure fallback failed:', e2);
                try { addMessage('❌ AudioDecoder configure failed; audio may not play.'); } catch (ignore) {}
                throw e2;
            }
        }

    if (numberOfChannels > 1) {
        // Build minimal OpusHead identification header (little-endian fields):
        // "OpusHead" (8 bytes) | version (1) | channels (1) |
        // pre-skip (uint16 little-endian) | sample rate (uint32 little-endian) |
        // output gain (uint16 little-endian) | channel mapping (1)
        // Use pre-skip=312 (common), sampleRate=48000, outputGain=0, mapping=0
        const desc = new Uint8Array([
            0x4f, 0x70, 0x75, 0x73, 0x48, 0x65, 0x61, 0x64, // "OpusHead"
            0x01, // version
            numberOfChannels & 0xff,
            0x38, 0x01, // pre-skip = 312 (0x0138 little-endian)
            0x80, 0xbb, 0x00, 0x00, // sample rate 48000 (0x0000BB80 little-endian)
            0x00, 0x00, // output gain
            0x00 // channel mapping
        ]);
        cfg.description = desc.buffer;
    }

    decoder.configure(cfg);

    // Remember configured channel count on the decoder instance so callers
    // can check and recreate the decoder when channel count changes.
    try {
        decoder._numberOfChannels = numberOfChannels;
    } catch (e) {
        // Not critical if we can't attach property in some environments
    }

    return decoder;
}
let audioTimestamp = 0; // Track continuous timestamp

// Cached DOM elements for performance
let cachedElements = {
    rxBitrate: null,
    txBitrate: null,
    rxDuration: null,
    txDuration: null,
    rxBytes: null,
    txBytes: null,
    rxChannels: null,
    rxSource: null,
    activeChannels: null,
    connectedUsers: null,
    totalClients: null
};

// Initialize cached DOM elements
function initializeDOMCache() {
    cachedElements.rxBitrate = document.getElementById('rx-bitrate');
    cachedElements.txBitrate = document.getElementById('tx-bitrate');
    cachedElements.rxDuration = document.getElementById('rx-duration');
    cachedElements.txDuration = document.getElementById('tx-duration');
    cachedElements.rxBytes = document.getElementById('rx-bytes');
    cachedElements.txBytes = document.getElementById('tx-bytes');
    cachedElements.rxChannels = document.getElementById('rx-channels');
    cachedElements.rxSource = document.getElementById('rx-source');
    cachedElements.activeChannels = document.getElementById('active-channels');
    cachedElements.connectedUsers = document.getElementById('connected-users');
    cachedElements.totalClients = document.getElementById('total-clients');
}

// Audio statistics tracking
let audioStats = {
    transmitting: false,
    receiving: false,
    transmissionStartTime: 0,
    receptionStartTime: 0,
    transmittedBytes: 0,
    receivedBytes: 0,
    transmissionDuration: 0,
    receptionDuration: 0,
    currentTransmitBitrate: 0,
    currentReceiveBitrate: 0,
    activeSpeakers: new Map(),
    currentReceivingSource: null,
    lastAudioReceived: 0,
    initialized: false,
    lastValidRxBitrate: 0,
    lastValidTxBitrate: 0
};

// Store latest metadata (Vorbis comments / stream title) per channel
const channelMeta = new Map(); // channelID -> { comments: string, title: string }
let statsUpdateInterval = null;

// DOM Elements
const messagesDiv = document.getElementById('messages');
const connectionStatus = document.getElementById('connection-status');
const connectionText = document.getElementById('connection-text');
const userInfo = document.getElementById('user-info');
const pttButton = document.getElementById('ptt-button');

// Store metadata for the next binary audio frame
let currentAudioMetadata = null;

// Audio statistics functions
function startTransmissionStats() {
    audioStats.transmitting = true;
    audioStats.transmissionStartTime = performance.now();
    audioStats.transmittedBytes = 0;
    audioStats.currentTransmitBitrate = 0;
    
    // Stats are always visible now, just update the visual state
    document.getElementById('connection-indicator').style.background = '#dc3545'; // Red when transmitting
    
    if (!statsUpdateInterval) {
        statsUpdateInterval = setInterval(updateStatsDisplay, 100); // Update every 100ms
    }
}

function stopTransmissionStats() {
    audioStats.transmitting = false;
    document.getElementById('transmission-stats').style.opacity = '0.5';
    
    // Update connection indicator color
    if (!audioStats.receiving) {
        document.getElementById('connection-indicator').style.background = '#28a745'; // Green when idle
    }
    
    // Keep stats running if receiving, otherwise continue with periodic updates
    if (!audioStats.receiving && statsUpdateInterval) {
        // Don't clear interval - keep stats always updating for persistent display
    }
}

function startReceptionStats() {
    audioStats.receiving = true;
    audioStats.receptionStartTime = performance.now();
    audioStats.receivedBytes = 0;
    audioStats.currentReceiveBitrate = 0;
    audioStats.lastAudioReceived = performance.now();
    
    // Stats are always visible, just update visual state
    document.getElementById('reception-stats').style.opacity = '1';
    document.getElementById('connection-indicator').style.background = '#28a745'; // Green when receiving
    
    if (!statsUpdateInterval) {
        statsUpdateInterval = setInterval(updateStatsDisplay, 100);
    }
}

function stopReceptionStats() {
    audioStats.receiving = false;
    document.getElementById('reception-stats').style.opacity = '0.5';
    
    // Update connection indicator
    if (!audioStats.transmitting) {
        document.getElementById('connection-indicator').style.background = '#28a745'; // Green when idle
    }
    
            // reception stopped
    // Don't clear the interval - we want continuous updates
}

function updateStatsDisplay() {
    const now = performance.now();
    
    // Initialize stats elements if not done already
    if (!audioStats.initialized) {
        if (cachedElements.rxBitrate) cachedElements.rxBitrate.textContent = '0.0 kbps';
        if (cachedElements.txBitrate) cachedElements.txBitrate.textContent = '0.0 kbps';
        audioStats.initialized = true;
    }
    
    if (audioStats.transmitting) {
        audioStats.transmissionDuration = (now - audioStats.transmissionStartTime) / 1000;
        
        // Calculate TX bitrate using extracted validation functions
        const txDuration = audioStats.transmissionDuration;
        const txBytes = audioStats.transmittedBytes;
        
        audioStats.currentTransmitBitrate = calculateSafeBitrate(txDuration, txBytes, audioStats.lastValidTxBitrate);
        
        // Store as last valid value if current calculation succeeded
        if (isValidReceptionState(txDuration, txBytes) && audioStats.currentTransmitBitrate > 0) {
            audioStats.lastValidTxBitrate = audioStats.currentTransmitBitrate;
        }
        
        // Simple TX display updates using last valid values
        const safeTxDuration = isFinite(audioStats.transmissionDuration) ? audioStats.transmissionDuration.toFixed(1) : '0.0';
        const safeTxBitrate = isFinite(audioStats.currentTransmitBitrate) ? audioStats.currentTransmitBitrate.toFixed(1) : '0.0';
        
        updateElementText('tx-duration', safeTxDuration + 's');
        updateElementText('tx-bytes', formatBytes(audioStats.transmittedBytes || 0));
        updateElementText('tx-bitrate', safeTxBitrate + ' kbps');
    }
    
    if (audioStats.receiving) {
        // Check if audio reception has timed out (no audio for 3 seconds)
        if (audioStats.lastAudioReceived > 0 && (now - audioStats.lastAudioReceived) > 3000) {
            stopReceptionStats();
            return;
        }
        
        // Early validation to prevent any invalid calculations
        if (!audioStats.receptionStartTime || audioStats.receptionStartTime <= 0 || now <= audioStats.receptionStartTime) {
            // Invalid start time, skip calculation
            updateElementText('rx-bitrate', '0.0 kbps');
            return;
        }
        
        audioStats.receptionDuration = (now - audioStats.receptionStartTime) / 1000;
        
        // Calculate bitrate using extracted validation functions
        const duration = audioStats.receptionDuration;
        const bytes = audioStats.receivedBytes;
        
        audioStats.currentReceiveBitrate = calculateSafeBitrate(duration, bytes, audioStats.lastValidRxBitrate);
        
        // Store as last valid value if current calculation succeeded
        if (isValidReceptionState(duration, bytes) && audioStats.currentReceiveBitrate > 0) {
            audioStats.lastValidRxBitrate = audioStats.currentReceiveBitrate;
        }
        
        // Simple display updates using last valid values
        const safeDuration = isFinite(audioStats.receptionDuration) ? audioStats.receptionDuration.toFixed(1) : '0.0';
        const safeBitrate = isFinite(audioStats.currentReceiveBitrate) ? audioStats.currentReceiveBitrate.toFixed(1) : '0.0';
        
        updateElementText('rx-duration', safeDuration + 's');
        updateElementText('rx-bytes', formatBytes(audioStats.receivedBytes || 0));
        updateElementText('rx-bitrate', safeBitrate + ' kbps');
        updateElementText('rx-source', audioStats.currentReceivingSource || 'Unknown');
    }
}

function isValidReceptionState(duration, bytes) {
    return duration > MIN_RECEPTION_DURATION && bytes > 0;
}

function calculateSafeBitrate(duration, bytes, lastValidBitrate) {
    if (!isValidReceptionState(duration, bytes)) {
        return lastValidBitrate || 0;
    }
    
    const calculatedBitrate = (bytes * 8) / (duration * 1000);
    
    return getValidBitrate(calculatedBitrate, lastValidBitrate);
}

function getValidBitrate(calculated, lastValid) {
    if (isFinite(calculated) && calculated >= 0) {
        return calculated;
    }
    return lastValid || 0;
}

function formatBytes(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

function updateOpusStatus() {
    const statusDiv = document.getElementById('opus-status');
    
    if (webCodecsSupported) {
        statusDiv.textContent = '✅ WebCodecs Opus encoding/decoding ready';
        statusDiv.style.color = '#4CAF50';
    } else {
        statusDiv.textContent = '❌ WebCodecs API not supported in this browser';
        statusDiv.style.color = '#f44336';
    }
}

// --- Core Functions ---

async function initializeAudio() {
    try {
        addMessage('Requesting microphone access...');
        mediaStream = await navigator.mediaDevices.getUserMedia({
            audio: {
                echoCancellation: true,
                noiseSuppression: true,
                autoGainControl: true, // Let browser handle this for simplicity
                sampleRate: 48000,     // Request 48kHz if possible
                channelCount: 1
            }
        });
        microphonePermission = true;
        addMessage('✅ Microphone access granted');
        await initializeWebCodecs(); // Initialize WebCodecs after getting mic stream
        updatePTTButtonState();
    } catch (error) {
        addMessage('❌ Microphone access denied: ' + error.message);
        microphonePermission = false;
        updatePTTButtonState();
    }
}

async function initializeWebCodecs() {
    // Check WebCodecs API support
    if (!window.AudioEncoder || !window.AudioDecoder) {
        addMessage('❌ WebCodecs API not supported. Opus audio unavailable.');
        webCodecsSupported = false;
        updateOpusStatus();
        return;
    }

    try {
        // Check Opus codec support
        const encoderSupport = await AudioEncoder.isConfigSupported({
            codec: 'opus',
            sampleRate: OPUS_SAMPLE_RATE,
            numberOfChannels: 1,
            bitrate: OPUS_BITRATE
        });
        
        const decoderSupport = await AudioDecoder.isConfigSupported({
            codec: 'opus',
            sampleRate: OPUS_SAMPLE_RATE,
            numberOfChannels: 1
        });

        if (!encoderSupport.supported || !decoderSupport.supported) {
            addMessage('❌ Opus codec not supported by WebCodecs.');
            webCodecsSupported = false;
            updateOpusStatus();
            return;
        }

        addMessage('Initializing WebCodecs Opus encoder/decoder...');
        
        // Initialize encoder
        webCodecsEncoder = new AudioEncoder({
            output: (encodedChunk) => {
                if (ws && ws.readyState === WebSocket.OPEN) {
                    // Send immediately for low latency
                    sendOpusAudioChunk(encodedChunk);
                }
            },
            error: (err) => {
                addMessage('❌ Encoder error: ' + err.message);
            }
        });
        
        webCodecsEncoder.configure({
            codec: 'opus',
            sampleRate: OPUS_SAMPLE_RATE,
            numberOfChannels: 1,
            bitrate: OPUS_BITRATE
        });
        
        // Initialize decoder
        webCodecsDecoder = createAudioDecoder(
            (audioData) => {
                playDecodedAudio(audioData);
            },
            (err) => {
                addMessage('❌ Decoder error: ' + err.message);
            }
        , 1
        );
        webCodecsDecoderChannels = 1;
        
        webCodecsSupported = true;
        addMessage('✅ WebCodecs Opus encoder/decoder initialized successfully.');
        updateOpusStatus(); // Update status display
        
    } catch (err) {
        addMessage('❌ Failed to initialize WebCodecs: ' + err.message);
        webCodecsSupported = false;
        updateOpusStatus(); // Update status display
    }
}

function resample(inputData, inputSampleRate, outputSampleRate) {
    if (inputSampleRate === outputSampleRate) {
        return inputData;
    }
    const ratio = inputSampleRate / outputSampleRate;
    const outputLength = Math.floor(inputData.length / ratio);
    const outputData = new Float32Array(outputLength);
    for (let i = 0; i < outputLength; i++) {
        const inputIndex = i * ratio;
        const index1 = Math.floor(inputIndex);
        const index2 = index1 + 1;
        const fraction = inputIndex - index1;
        const sample1 = inputData[index1] || 0;
        const sample2 = index2 < inputData.length ? inputData[index2] : 0;
        outputData[i] = sample1 + (sample2 - sample1) * fraction; // Linear interpolation
    }
    return outputData;
}

function startAudioCapture() {
    if (!microphonePermission || !mediaStream) {
        addMessage('⚠️ No microphone - sending PTT event only.');
        return;
    }

    try {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            audioContext.resume();
        }
        
        const source = audioContext.createMediaStreamSource(mediaStream);
        // Use standard buffer size for good latency/performance balance
        const processor = audioContext.createScriptProcessor(2048, 1, 1);
        
        if (!webCodecsSupported || !webCodecsEncoder) {
            addMessage('❌ Cannot start audio capture: WebCodecs Opus not available');
            return;
        }
        
        addMessage(`🎵 Capturing at ${audioContext.sampleRate}Hz, encoding with WebCodecs Opus.`);
        
        processor.onaudioprocess = function(event) {
            if (!isPTTActive || !webCodecsEncoder) return;

            const inputData = event.inputBuffer.getChannelData(0);
            
            // Resample to 48kHz for Opus if needed
            const resampledData = audioContext.sampleRate !== OPUS_SAMPLE_RATE ? 
                resample(inputData, audioContext.sampleRate, OPUS_SAMPLE_RATE) : 
                inputData;
            
            // Create AudioData for WebCodecs with proper timestamp
            const audioData = new AudioData({
                format: 'f32-planar',
                sampleRate: OPUS_SAMPLE_RATE,
                numberOfFrames: resampledData.length,
                numberOfChannels: 1,
                timestamp: audioTimestamp,
                data: resampledData
            });
            
            // Update timestamp for next frame (microseconds)
            audioTimestamp += (resampledData.length * 1000000) / OPUS_SAMPLE_RATE;
            
            webCodecsEncoder.encode(audioData);
            audioData.close(); // Important: release memory
        };
        
        source.connect(processor);
        // Mute the processor output to prevent feedback
        const muteGain = audioContext.createGain();
        muteGain.gain.value = 0;
        processor.connect(muteGain);
        muteGain.connect(audioContext.destination);
        
        window.audioSource = source;
        window.audioProcessor = processor;
        
        addMessage('🎤 Opus audio capture started.');
        
    } catch (error) {
        addMessage('❌ Audio capture failed: ' + error.message);
    }
}

function stopAudioCapture() {
    if (window.audioProcessor) {
        window.audioProcessor.disconnect();
        window.audioProcessor.onaudioprocess = null;
    }
    if (window.audioSource) {
        window.audioSource.disconnect();
    }
    window.audioProcessor = null;
    window.audioSource = null;
    nextPlayTime = 0;
    
    addMessage('🎤 Opus audio capture stopped.');
}

function sendOpusAudioChunk(encodedChunk) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        const byteLength = encodedChunk.byteLength;
        const audioEvent = {
            type: 'audio_data',
            timestamp: new Date().toISOString(),
            data: {
                chunk_size: byteLength,
                format: 'opus',
                sample_rate: OPUS_SAMPLE_RATE,
                channels: 1
            }
        };
        
        ws.send(JSON.stringify(audioEvent));
        
        // Send encoded chunk efficiently
        let buffer;
        if (typeof encodedChunk.detach === 'function') {
            buffer = encodedChunk.detach();
        } else {
            buffer = new ArrayBuffer(encodedChunk.byteLength);
            encodedChunk.copyTo(new Uint8Array(buffer));
        }
        ws.send(buffer);
        
        // Update transmission stats
        audioStats.transmittedBytes += byteLength;
        
        if (Math.random() < AUDIO_CHUNK_LOG_SAMPLE_RATE) {
            addMessage('📤 Sent Opus chunk: ' + byteLength + ' bytes');
        }
    } else {
        addMessage('❌ WebSocket not ready for audio transmission');
    }
}


function initializeAudioMixer() {
    if (!audioContext) {
        audioContext = new (window.AudioContext || window.webkitAudioContext)();
    }
    if (!audioMixerGain) {
        audioMixerGain = audioContext.createGain();
        audioMixerGain.gain.value = 0.8; // Set overall volume to prevent clipping
        audioMixerGain.connect(audioContext.destination);
        
        // Initialize nextPlayTime to current audio context time
        nextPlayTime = audioContext.currentTime;
        
        addMessage('🎚️ Audio mixer initialized');
    }
}


async function startBufferedPlayback() {
    if (isPlayingBuffer) return;
    isPlayingBuffer = true;
    addMessage('🎵 Starting buffered audio playback with ~100ms delay');

    while (audioBuffer.length > 0) {
        const chunk = audioBuffer.shift();
        
        try {
            await playAudioWithMixingBuffered(chunk.data, chunk.sampleRate, chunk.userID);
            
            // Wait a bit between chunks for smoother playback
            await new Promise(resolve => setTimeout(resolve, 15));
            
        } catch (error) {
            addMessage(`⚠️ Buffer playback error: ${error.message}`);
        }
        
        // If buffer gets too low, pause until it fills up again
        if (audioBuffer.length < 2) {
            await new Promise(resolve => setTimeout(resolve, 50));
        }
    }
    
    isPlayingBuffer = false;
    addMessage('🔇 Buffered audio playback stopped');
}

async function playDecodedAudio(audioData) {
    try {
        const numberOfFrames = audioData.numberOfFrames;
        const numberOfChannels = audioData.numberOfChannels;
        const sampleRate = audioData.sampleRate;

        // Initialize audio context if needed
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            await audioContext.resume();
        }
        if (!audioMixerGain) {
            initializeAudioMixer();
        }

        // Create Web Audio buffer directly from AudioData
        const audioBufferNode = audioContext.createBuffer(numberOfChannels, numberOfFrames, sampleRate);

        // Copy audio data channel-by-channel to ensure proper channel mapping
        for (let ch = 0; ch < numberOfChannels; ch++) {
            const channelData = audioBufferNode.getChannelData(ch);
            try {
                // Copy planar f32 data directly to each channel
                audioData.copyTo(channelData, { planeIndex: ch, format: 'f32-planar' });
            } catch (e) {
                // Fallback: if planar copy fails, try interleaved and deinterleave
                const interleaved = new Float32Array(numberOfFrames * numberOfChannels);
                audioData.copyTo(interleaved, { format: 'f32' });
                
                for (let i = 0; i < numberOfFrames; i++) {
                    channelData[i] = interleaved[i * numberOfChannels + ch] || 0;
                }
            }
        }

        // Create and schedule audio playback with proper timing
        const userGain = audioContext.createGain();
        userGain.gain.value = 0.7; // Conservative volume to prevent clipping
        
        const source = audioContext.createBufferSource();
        source.buffer = audioBufferNode;
        source.connect(userGain);
        userGain.connect(audioMixerGain);

        // Use simple immediate playback for real-time audio - no complex buffering
        // Web Audio will handle the timing internally with its own buffering
        const currentTime = audioContext.currentTime;
        const playTime = Math.max(currentTime, nextPlayTime);
        
        source.start(playTime);
        
        // Update next play time based on actual audio duration
        const frameDuration = numberOfFrames / sampleRate;
        nextPlayTime = playTime + frameDuration;

        source.onended = () => {
            userGain.disconnect();
        };

        // Update audio stats with more detailed info
        if (audioData.duration > 0) {
            const bitrate = (numberOfFrames * numberOfChannels * 32) / (audioData.duration / 1000000) / 1000; // Estimate based on f32 PCM
            updateElementText('rx-bitrate', `${bitrate.toFixed(1)} kbps`);
        }
        const channelText = numberOfChannels === 1 ? 'Mono' : `${numberOfChannels}-channel`;
        updateElementText('rx-channels', `${channelText} @ ${sampleRate}Hz`);

    } catch (error) {
        addMessage(`❌ Error playing decoded audio: ${error.message}`);
    } finally {
        // Ensure cleanup happens regardless of success or failure
        if (audioData && typeof audioData.close === 'function') {
            try {
                audioData.close();
            } catch (closeError) {
                console.warn('Error during AudioData cleanup:', closeError);
            }
        }
    }
}

async function playAudioWithMixingBuffered(pcmData, sampleRate, userID) {
    try {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            await audioContext.resume();
        }
        if (!audioMixerGain) {
            initializeAudioMixer();
        }

        if (!pcmData) return;

        // pcmData may be a Float32Array (mono) or an Array of Float32Array (per-channel)
        let numChannels = 1;
        let length = 0;
        if (Array.isArray(pcmData)) {
            numChannels = pcmData.length;
            length = pcmData[0].length;
        } else {
            numChannels = 1;
            length = pcmData.length;
        }

        const audioBufferNode = audioContext.createBuffer(numChannels, length, sampleRate);

        if (numChannels === 1) {
            audioBufferNode.getChannelData(0).set(pcmData);
        } else {
            for (let ch = 0; ch < numChannels; ch++) {
                audioBufferNode.getChannelData(ch).set(pcmData[ch]);
            }
        }

        const userGain = audioContext.createGain();
        userGain.gain.value = 0.6; // Conservative volume for buffered playback

        const source = audioContext.createBufferSource();
        source.buffer = audioBufferNode;
        source.connect(userGain);
        userGain.connect(audioMixerGain);

        // Play immediately without complex scheduling
        source.start();

        source.onended = () => {
            userGain.disconnect();
        };

    } catch (error) {
        addMessage(`⚠️ Buffered audio playback failed for user ${userID}: ${error.message}`);
    }
}

async function playAudioWithMixing(pcmData, sampleRate, userID) {
    try {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }
        if (audioContext.state === 'suspended') {
            await audioContext.resume();
        }
        if (!audioMixerGain) {
            initializeAudioMixer();
        }

        if (!pcmData || (Array.isArray(pcmData) ? pcmData.length === 0 : pcmData.length === 0)) return;

        // Add to buffer for smooth playback - use consistent structure
        // Store both pcmData (Float32Array or Array of Float32Array) and channel count
        const numChannels = Array.isArray(pcmData) ? pcmData.length : 1;
        audioBuffer.push({ 
            data: pcmData, 
            sampleRate: sampleRate, 
            userID: userID,
            channels: numChannels,
            timestamp: performance.now()
        });
        
        // Start buffered playback if not already running
        if (!isBuffering) {
            processAudioBuffer();
        }

    } catch (error) {
        addMessage(`⚠️ Audio playback failed for user ${userID}: ${error.message}`);
    }
}

async function processAudioBuffer() {
    if (isBuffering) return;
    isBuffering = true;
    
    try {
        while (audioBuffer.length > 0) {
            // Wait for buffer to build up slightly for smoother playback
            if (audioBuffer.length < 3 && audioBuffer.length > 0) {
                await new Promise(resolve => setTimeout(resolve, 10)); // 10ms wait
                continue;
            }
            
            if (audioBuffer.length === 0) break;
            
            const { data: pcmData, sampleRate, userID, channels } = audioBuffer.shift();
            
            const numChannels = channels || (Array.isArray(pcmData) ? pcmData.length : 1);
            const length = numChannels === 1 ? pcmData.length : pcmData[0].length;

            const audioBufferNode = audioContext.createBuffer(numChannels, length, sampleRate);

            if (numChannels === 1) {
                audioBufferNode.getChannelData(0).set(pcmData);
            } else {
                for (let ch = 0; ch < numChannels; ch++) {
                    audioBufferNode.getChannelData(ch).set(pcmData[ch]);
                }
            }

            const userGain = audioContext.createGain();
            userGain.gain.value = 0.8;

            const source = audioContext.createBufferSource();
            source.buffer = audioBufferNode;
            source.connect(userGain);
            userGain.connect(audioMixerGain);

            // Schedule proper playback timing for streaming audio
            const currentTime = audioContext.currentTime;
            const bufferTime = 0.1; // 100ms buffer for smoother playback
            
            if (nextPlayTime < currentTime + bufferTime) {
                nextPlayTime = currentTime + bufferTime;
            }
            
            source.start(nextPlayTime);
            
            // Update next play time based on audio duration
            const frameDuration = length / sampleRate;
            nextPlayTime += frameDuration;

            source.onended = () => {
                userGain.disconnect();
            };
            
            // Small delay to prevent overwhelming the audio system
            await new Promise(resolve => setTimeout(resolve, 5));
        }
    } catch (error) {
        addMessage(`⚠️ Audio buffer processing failed: ${error.message}`);
    } finally {
        isBuffering = false;
        
        // Continue processing if more audio arrived while we were processing
        if (audioBuffer.length > 0) {
            processAudioBuffer();
        }
    }
}

async function playReceivedAudio(audioData, userID = 'unknown', metadata) {
    try {
        // metadata is preferred, but derive format/channels from header if missing
        if (!metadata) {
            metadata = { data: {} };
        }

        let pcmData;
    const format = metadata.data.format;
    let sampleRate = metadata.data.sample_rate;
        
        // Start reception stats if not already started
        if (!audioStats.receiving) {
            // Try to get username from various places in metadata
            const sourceName = (metadata.data && metadata.data.username) || 
                              (currentAudioMetadata && currentAudioMetadata.data && currentAudioMetadata.data.username) ||
                              userID;
            audioStats.currentReceivingSource = sourceName;
            startReceptionStats();
        }
        
        // Parse and strip the server-side 15-byte header (seq:uint32, ts:uint64, channels:uint8, payload_len:uint16)
        // so we feed raw Opus payloads to WebCodecs. Be robust to ArrayBuffer/Uint8Array/Buffer.
        let buf;
        if (audioData instanceof ArrayBuffer) {
            buf = new Uint8Array(audioData);
        } else if (audioData instanceof Uint8Array) {
            buf = audioData;
        } else {
            try {
                buf = new Uint8Array(audioData);
            } catch (e) {
                addMessage('⚠️ Received audio in unknown format, skipping');
                return;
            }
        }

        let payloadBuf = buf;
        // Ensure tsNumber exists even if header parsing is skipped
        let tsNumber = 0;
        let seq = 0; // Initialize seq in outer scope for timestamp calculations
        const HEADER_LEN = 15;
        if (buf.length >= HEADER_LEN) {
            const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
            seq = dv.getUint32(0, false);
            const channelsFromHeader = dv.getUint8(12);
            const payloadLen = dv.getUint16(13, false); // big-endian
            // Dedupe by sequence number (helps if server forwards duplicate frames)
            if (recentSeqs.has(seq)) {
                if (JS_DEBUG_AUDIO) console.debug('Skipping duplicate seq', seq);
                return; // ignore duplicate frame
            }
            // record seq with timestamp
            recentSeqs.set(seq, performance.now());
            // keep map bounded
            if (recentSeqs.size > RECENT_SEQS_MAX) {
                // remove oldest entry
                let oldestKey = null;
                let oldestVal = Infinity;
                for (const [k, v] of recentSeqs.entries()) {
                    if (v < oldestVal) {
                        oldestVal = v;
                        oldestKey = k;
                    }
                }
                if (oldestKey !== null) recentSeqs.delete(oldestKey);
            }
            // Read server-supplied timestamp (big-endian uint64 microseconds)
            let tsUs = 0n;
            if (typeof dv.getBigUint64 === 'function') {
                try {
                    tsUs = dv.getBigUint64(4, false);
                } catch (e) {
                    // fallback
                }
            }
            if (tsUs === 0n) {
                // fallback to combining two 32-bit reads
                const hi = dv.getUint32(4, false);
                const lo = dv.getUint32(8, false);
                tsUs = BigInt(hi) << 32n | BigInt(lo);
            }
            const tsNumber = Number(tsUs);

            if (JS_DEBUG_AUDIO) {
                const tsRel = (serverTsBaseUs === null ? 0 : tsNumber - serverTsBaseUs);
                console.debug('AUDIO IN header:', { channelsFromHeader, payloadLen, bufLen: buf.length, tsUs: tsNumber, tsRel });
            }

            // Prefer the header-provided channel count when available.
            // This ensures we recreate the decoder to match the actual
            // incoming packet channel layout (important for stereo).
            if (metadata && metadata.data) {
                if (channelsFromHeader && channelsFromHeader > 0) {
                    metadata.data.channels = Number(channelsFromHeader);
                } else if (!metadata.data.channels) {
                    metadata.data.channels = 1;
                }
            }

            // Populate format/sample_rate defaults if missing
            if (metadata && metadata.data) {
                if (!metadata.data.format) metadata.data.format = 'opus';
                if (!metadata.data.sample_rate) metadata.data.sample_rate = 48000;
            }

            if (buf.length >= HEADER_LEN + payloadLen) {
                payloadBuf = buf.subarray(HEADER_LEN, HEADER_LEN+payloadLen);
            } else {
                // fallback: use remaining bytes as payload if lengths don't match
                payloadBuf = buf.subarray(HEADER_LEN);
            }
        }

        // Update reception stats with actual payload size when possible
        audioStats.receivedBytes += payloadBuf.byteLength || payloadBuf.length || 0;
        audioStats.lastAudioReceived = performance.now();

    // Validate format, but if missing try to infer from header later
    // We'll defer strict format validation until after header parsing
        
        if (!webCodecsSupported) {
            addMessage(`⚠️ WebCodecs not supported, skipping audio from ${userID}`);
            return;
        }

    // Ensure decoder is ready and recreate if needed
    const expectedChannels = Number(metadata.data && metadata.data.channels ? metadata.data.channels : 1);
    const currentDecoderChannels = webCodecsDecoder ? webCodecsDecoderChannels : 0;
    if (!webCodecsDecoder || webCodecsDecoder.state === 'closed' || webCodecsDecoder.state === 'unconfigured' || (currentDecoderChannels && currentDecoderChannels !== expectedChannels)) {
            try {
                webCodecsDecoder = createAudioDecoder(
                    (audioData) => {
                        playDecodedAudio(audioData);
                    },
                    (err) => {
                        addMessage(`❌ Decoder error from ${userID}: ${err.message}`);
                        // Reset decoder on error for next attempt
                        webCodecsDecoder = null;
                    },
                    expectedChannels
                );

                // Track configured channels globally for reliable comparisons
                webCodecsDecoderChannels = expectedChannels;

                addMessage(`🔧 Recreated WebCodecs decoder for ${userID} (channels=${expectedChannels})`);
                // Reset playback timing and server timestamp base for new decoder instance
                serverTsBaseUs = null;
                nextPlayTime = 0;
            } catch (err) {
                addMessage(`❌ Failed to recreate decoder for ${userID}: ${err.message}`);
                return;
            }
        }
        
        try {
            // Check decoder state before attempting decode
            if (webCodecsDecoder.state !== 'configured') {
                addMessage(`❌ Decoder not configured (state: ${webCodecsDecoder.state}), skipping audio from ${userID}`);
                webCodecsDecoder = null; // Force recreation on next packet
                return;
            }

            // Standard Opus frame duration is 20ms (20000 microseconds)
            // Individual Opus packets are typically 20ms frames
            const estimatedDuration = 20000;
            
            // Create EncodedAudioChunk for WebCodecs decoder with proper timestamp
            // Use the stripped payload buffer only.
            const ab = payloadBuf.buffer.slice(payloadBuf.byteOffset, payloadBuf.byteOffset + payloadBuf.byteLength);
            
            // For WebCodecs, use incremental timestamps instead of server timestamps
            // This avoids timing discontinuities and ensures smooth playback
            const FRAME_DURATION_US = 20000; // 20ms in microseconds for typical Opus frames
            
            // Use simple incremental timestamps for WebCodecs decoder
            // This ensures consistent timing without jumps or discontinuities
            let decoderTimestamp;
            if (serverTsBaseUs === null) {
                serverTsBaseUs = tsNumber;
                decoderTimestamp = 0;
            } else {
                // Calculate expected timestamp based on frame sequence
                // This provides stable timing for the decoder
                decoderTimestamp = (seq - Math.floor(serverTsBaseUs / FRAME_DURATION_US)) * FRAME_DURATION_US;
                
                // Ensure timestamp always advances
                if (decoderTimestamp <= 0) {
                    decoderTimestamp = seq * FRAME_DURATION_US;
                }
            }
            
            const encodedChunk = new EncodedAudioChunk({
                type: 'key', // Required property for Opus frames
                timestamp: decoderTimestamp, // Use stable incremental timestamp
                data: ab,
                duration: FRAME_DURATION_US
            });
            
            webCodecsDecoder.decode(encodedChunk);
            // Note: decoded audio will be handled in the decoder's output callback
        } catch (err) {
            addMessage(`❌ Failed to decode Opus from ${userID}: ${err.message}`);
            // Reset decoder on any decode error for next attempt
            if (err.message.includes('closed codec') || err.message.includes('unconfigured codec')) {
                webCodecsDecoder = null;
                addMessage(`🔧 Decoder error detected, will recreate on next audio`);
            }
        }

    } catch (error) {
        addMessage(`❌ Error processing received audio from ${userID}: ${error.message}`);
    }
}

// --- WebSocket and UI Functions ---

function connect() {
    if (ws) return;
    
    ws = new WebSocket('ws://localhost:8080/ws');
    
    let heartbeatInterval = null;
    ws.onopen = function() {
        connectionStatus.classList.add('connected');
        connectionText.textContent = 'Connected';
        addMessage('Connected to server');
        updateStats();
        // Start app-level heartbeat every 20s to help intermediaries and
        // clients that do not surface control frames to the server.
        heartbeatInterval = setInterval(() => {
            if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(JSON.stringify({ type: 'heartbeat' }));
            }
        }, 20000);
    };
    
    ws.onmessage = function(event) {
        if (event.data instanceof ArrayBuffer) {
            const userID = currentAudioMetadata ? currentAudioMetadata.user_id : 'unknown';
            playReceivedAudio(event.data, userID, currentAudioMetadata);
            currentAudioMetadata = null; // Clear after use
            
        } else if (event.data instanceof Blob) {
            const userID = currentAudioMetadata ? currentAudioMetadata.user_id : 'unknown';
            const metadataForBlob = currentAudioMetadata;
            currentAudioMetadata = null;
            
            event.data.arrayBuffer().then(buffer => {
                playReceivedAudio(buffer, userID, metadataForBlob);
            });
            
        } else if (typeof event.data === 'string') {
            try {
                const data = JSON.parse(event.data);
                
                if (data.type === 'audio_data') {
                    currentAudioMetadata = data; // Store for the next binary frame
                    return;
                }
                
                handleServerEvent(data);
            } catch (e) {
                addMessage('Received non-JSON text: ' + event.data);
            }
        } else {
            addMessage('Received unknown data type: ' + typeof event.data);
        }
    };
    
    ws.onclose = function() {
        connectionStatus.classList.remove('connected');
        connectionText.textContent = 'Disconnected';
        userInfo.textContent = '';
        addMessage('Disconnected from server. Reconnecting in 3s...');
        pttButton.disabled = true;
        ws = null;
        if (heartbeatInterval) {
            clearInterval(heartbeatInterval);
            heartbeatInterval = null;
        }
        setTimeout(connect, 3000);
    };
    
    ws.onerror = function(error) {
        addMessage('Connection error: ' + JSON.stringify(error));
    };
}

function handleServerEvent(event) {
    addMessage(`Event: ${event.type} from ${event.user_id ? event.user_id.substring(0,8) : 'server'} ${event.data ? '- ' + JSON.stringify(event.data) : ''}`);
    
    switch (event.type) {
        case 'user_join':
            if (event.user_id && event.data && event.data.username) {
                if (!currentUser) { // Assume first join event is for us
                    if (event.user_id) {
                        currentUser = {
                            id: event.user_id,
                            username: event.data.username
                        };
                        userInfo.textContent = ` - ${event.data.username}`;
                    }
                }
            }
            updateStats();
            updateUsersList();
            break;
            
        case 'user_leave':
            updateStats();
            updateUsersList();
            break;
            
        case 'channel_join':
            if (event.user_id === (currentUser ? currentUser.id : '')) {
                currentChannel = event.channel_id;
                document.getElementById('channel-input').value = currentChannel;
            }
            updatePTTButtonState();
            updateStats();
            updateChannelsList();
            updateUsersList();
            break;
            
        case 'channel_leave':
            if (event.user_id === (currentUser ? currentUser.id : '')) {
                currentChannel = '';
                document.getElementById('channel-input').value = '';
            }
            updatePTTButtonState();
            updateStats();
            updateChannelsList();
            updateUsersList();
            break;
            
        case 'ptt_start':
            addMessage(`🎙️ ${event.data.username} started talking in ${event.channel_id}`);
            highlightTalkingUser(event.user_id, true);
            highlightTalkingChannel(event.channel_id, true);
            updateChannelsList(); // Refresh channel list to update speaker count
            
            // Track active speakers
            audioStats.activeSpeakers.set(event.user_id, {
                username: event.data.username,
                channel: event.channel_id,
                startTime: performance.now()
            });
            updateActiveSpeakersDisplay();
            
            // Start reception stats if someone else is talking
            if (event.user_id !== (currentUser ? currentUser.id : '')) {
                audioStats.currentReceivingSource = event.data.username;
                startReceptionStats();
            }
            break;
            
        case 'ptt_end':
            addMessage(`🔇 ${event.data.username} stopped talking in ${event.channel_id}`);
            highlightTalkingUser(event.user_id, false);
            highlightTalkingChannel(event.channel_id, false);
            updateChannelsList(); // Refresh channel list to update speaker count
            
            // Remove from active speakers
            audioStats.activeSpeakers.delete(event.user_id);
            updateActiveSpeakersDisplay();
            
            // Stop reception stats if someone else stopped talking
            if (event.user_id !== (currentUser ? currentUser.id : '')) {
                audioStats.currentReceivingSource = null;
                stopReceptionStats();
            }
            break;
            
        case 'heartbeat':
            // Silent heartbeat response
            break;

        case 'meta':
            // Store Vorbis/OpusTags comments as stream title for the channel
            try {
                const comments = event.data && event.data.comments ? String(event.data.comments) : '';
                let title = '';
                if (comments) {
                    // Try a simple heuristic: look for TITLE= or StreamTitle=
                    const m = comments.match(/TITLE=(.+)/i) || comments.match(/StreamTitle=(.+)/i);
                    if (m && m[1]) title = m[1].trim();
                }
                channelMeta.set(event.channel_id || '', { comments: comments, title: title });
                addMessage(`🔖 Saved stream metadata for channel ${event.channel_id}: ${title || '(no title)'}`);
                // Refresh channel list UI to show title
                updateChannelsList();
            } catch (e) {
                console.warn('Failed to process meta event', e);
            }
            break;
            
        case 'error':
            // Handle server-side errors with user-friendly messages
            const errorMsg = escapeHtml(event.data.message || 'Unknown server error');
            const errorCode = escapeHtml(event.data.code || 'UNKNOWN');
            addMessage(`❌ Server Error [${errorCode}]: ${errorMsg}`);
            
            // Handle specific error types
            if (errorCode === 'NO_CHANNEL') {
                addMessage('💡 Please join a channel before transmitting audio');
            }
            break;
    }
}

function updatePTTButtonState() {
    if (!webCodecsSupported) {
        pttButton.disabled = true;
        pttButton.textContent = 'WEBCODECS NOT SUPPORTED';
    } else if (!microphonePermission) {
        pttButton.disabled = true;
        pttButton.textContent = 'GRANT MIC PERMISSION';
    } else if (!currentChannel) {
        pttButton.disabled = true;
        pttButton.textContent = 'JOIN A CHANNEL TO TALK';
    } else if (microphonePermission && currentChannel && ws && ws.readyState === WebSocket.OPEN) {
        const currentChannelCard = document.getElementById(`channel-card-${currentChannel}`);
        if (currentChannelCard && currentChannelCard.dataset.publishOnly === 'true') {
            pttButton.disabled = true;
            pttButton.textContent = 'CHANNEL IS READ-ONLY';
        } else {
            pttButton.disabled = false;
            pttButton.textContent = 'HOLD TO TALK (OPUS)';
        }
    } else {
        pttButton.disabled = true;
        pttButton.textContent = 'CONNECTING...';
    }
}

function sendEvent(eventType, data = {}) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        const event = {
            type: eventType,
            channel_id: currentChannel,
            timestamp: new Date().toISOString(),
            data: data
        };
        ws.send(JSON.stringify(event));
    }
}

function joinChannel() {
    const channelName = document.getElementById('channel-input').value.trim();
    if (!channelName) { 
        alert('Please enter a channel name');
        return;
    }
    
    sendEvent('channel_join', { channel_name: channelName });
}

function leaveChannel() {
    if (currentChannel) {
        sendEvent('channel_leave');
    }
}

function startPTT() {
    if (pttButton.disabled || isPTTActive) return;
    
    isPTTActive = true;
    pttButton.classList.add('active');
    
    // Start transmission stats tracking
    startTransmissionStats();
    
    if (microphonePermission) {
        pttButton.textContent = 'TRANSMITTING...';
        startAudioCapture();
    } else {
        pttButton.textContent = 'TRANSMITTING EVENT...';
    }
    
    sendEvent('ptt_start');
}

function endPTT() {
    if (!isPTTActive) return;
    
    isPTTActive = false;
    pttButton.classList.remove('active');
    
    // Stop transmission stats tracking
    stopTransmissionStats();
    
    if (microphonePermission) {
        stopAudioCapture();
    }
    
    updatePTTButtonState();
    sendEvent('ptt_end');
}

function updateStats() {
    fetch('/api/stats')
        .then(response => response.json())
        .then(stats => {
            updateElementText('active-channels', stats.active_channels);
            updateElementText('connected-users', stats.active_users);
            updateElementText('total-clients', stats.connected_clients);
        })
        .catch(err => console.error('Failed to fetch stats:', err));
}

function updateChannelsList() {
    fetch('/api/channels')
        .then(response => response.json())
        .then(data => {
            const channelGrid = document.getElementById('channel-grid');
            if (data.channels && data.channels.length > 0) {
                channelGrid.innerHTML = data.channels.map(channel => {
                    const activeSpeakerCount = channel.active_speakers ? Object.keys(channel.active_speakers).length : 0;
                    const isTalking = activeSpeakerCount > 0;
                    const isPublishOnly = channel.users.some(user => user.publish_only);
                    const publishOnlyClass = isPublishOnly ? 'publish-only' : '';
                    const isCurrentChannel = channel.id === currentChannel;
                    
                    // Audio info for talking channels
                    let audioInfo = '';
                    if (activeSpeakerCount > 0) {
                        audioInfo = `<div class="audio-info">🎤 ${activeSpeakerCount} speaking</div>`;
                    }
                    
                    // show stream title if demuxer provided metadata
                    const meta = channelMeta.get(channel.id) || {};
                    const titleLine = meta.title ? `<div class="channel-title">${escapeHtml(meta.title)}</div>` : '';
                    return `<div class="channel-card ${isCurrentChannel ? 'active' : ''} ${isTalking ? 'talking' : ''} ${publishOnlyClass}" 
                                 id="channel-card-${channel.id}"
                                 data-publish-only="${isPublishOnly}"
                                 onclick="selectChannel('${channel.id}')">
                                <div class="channel-status ${isCurrentChannel ? 'active' : ''} ${isTalking ? 'talking' : ''}"></div>
                                <div class="channel-name">${escapeHtml(channel.name)}</div>
                                ${titleLine}
                                <div class="channel-users">${channel.users.length} users</div>
                                ${audioInfo}
                                ${isPublishOnly ? '<div class="publish-only-indicator">Read-Only</div>' : ''}
                            </div>`;
                }).join('');
            } else {
                channelGrid.innerHTML = '<div style="color: #999; text-align: center; padding: 20px;">No active channels</div>';
            }
            updatePTTButtonState(); // Update PTT button state after updating channels
        })
        .catch(err => console.error('Failed to fetch channels:', err));
}

function updateUsersList() {
    fetch('/api/users')
        .then(response => response.json())
        .then(data => {
            const usersList = document.getElementById('users-list');
            if (data.users && data.users.length > 0) {
                usersList.innerHTML = data.users.map(user => 
                    `<div class="user-item ${user.is_active ? 'active' : ''}" id="user-${user.id}">
                        ${user.username}${user.channel ? ` (${user.channel})` : ''}
                    </div>`
                ).join('');
            } else {
                usersList.innerHTML = '<div style="color: #999; text-align: center; padding: 20px;">No connected users</div>';
            }
        })
        .catch(err => console.error('Failed to fetch users:', err));
}

function selectChannel(channelId) {
    // Don't switch if already in this channel
    if (currentChannel === channelId) {
        return;
    }
    
    // Leave current channel first if in one
    if (currentChannel) {
        sendEvent('channel_leave');
        // Small delay to ensure leave is processed
        setTimeout(() => {
            document.getElementById('channel-input').value = channelId;
            joinChannel();
        }, 100);
    } else {
        document.getElementById('channel-input').value = channelId;
        joinChannel();
    }
}

function highlightTalkingUser(userId, isTalking) {
    const userElement = document.getElementById(`user-${userId}`);
    if (userElement) {
        if (isTalking) {
            userElement.classList.add('talking');
        } else {
            userElement.classList.remove('talking');
        }
    }
}

function highlightTalkingChannel(channelId, isTalking) {
    const channelCard = document.getElementById(`channel-card-${channelId}`);
    if (channelCard) {
        if (isTalking) {
            channelCard.classList.add('talking');
        } else {
            channelCard.classList.remove('talking');
        }
    }
}

function updateActiveSpeakersDisplay() {
    const speakersList = document.getElementById('speakers-list');
    if (audioStats.activeSpeakers.size === 0) {
        speakersList.textContent = 'None';
    } else {
        const speakers = Array.from(audioStats.activeSpeakers.values())
            .map(speaker => `${speaker.username} (${speaker.channel})`)
            .join(', ');
        speakersList.textContent = speakers;
    }
}

function addMessage(message) {
    const div = document.createElement('div');
    div.textContent = new Date().toLocaleTimeString() + ' - ' + message;
    messagesDiv.appendChild(div);
    messagesDiv.scrollTop = messagesDiv.scrollHeight;
}

// Keyboard shortcuts
document.addEventListener('keydown', function(e) {
    if (e.code === 'Space' && !e.repeat && !pttButton.disabled) {
        e.preventDefault();
        pttButton.dispatchEvent(new MouseEvent('mousedown'));
    }
});

document.addEventListener('keyup', function(e) {
    if (e.code === 'Space') {
        e.preventDefault();
        pttButton.dispatchEvent(new MouseEvent('mouseup'));
    }
});

window.addEventListener('keydown', function(e) {
    if(e.code === 'Space' && e.target === document.body) {
        e.preventDefault();
    }
});

window.onload = function() {
    initializeDOMCache(); // Cache DOM elements for performance
    connect();
    initializeAudio();
    updateOpusStatus(); // Initialize Opus status display
    setInterval(updateStats, 2000);
    setInterval(updateChannelsList, 3000);
    setInterval(updateUsersList, 3000);
    
    // Start stats display immediately for always-on display
    if (!statsUpdateInterval) {
        statsUpdateInterval = setInterval(updateStatsDisplay, 100);
    }
};