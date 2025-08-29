let ws = null;
let currentUser = null;
let currentChannel = '';
let isPTTActive = false;

// Audio system variables
let audioContext = null;
let mediaStream = null;
let nextPlayTime = 0;
let activeAudioSources = new Map(); // Track audio sources by user ID
let decoderTimestamp = 0;
let audioBuffer = []; // Buffer for smooth playback
let isBuffering = false;
let isPlayingBuffer = false; // Track if we're currently playing buffered audio
let audioMixerGain = null;
let microphonePermission = false;

// WebCodecs Opus system variables
let webCodecsEncoder = null;
let webCodecsDecoder = null;
const OPUS_SAMPLE_RATE = 48000;
const OPUS_BITRATE = 32000;
const BUFFER_MAX_SIZE = 10; // Maximum buffer size to prevent memory issues
const BUFFER_TARGET_SIZE = 3; // Target buffer size for smooth playback
let webCodecsSupported = false;
let audioTimestamp = 0; // Track continuous timestamp

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
    document.getElementById('transmission-stats').style.opacity = '1';
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
    
    // Keep stats always running for persistent display
    // Don't clear the interval - we want continuous updates
}

function updateStatsDisplay() {
    const now = performance.now();
    
    // Initialize stats elements if not done already
    if (!audioStats.initialized) {
        const rxBitrateEl = document.getElementById('rx-bitrate');
        const txBitrateEl = document.getElementById('tx-bitrate');
        if (rxBitrateEl) rxBitrateEl.textContent = '0.0 kbps';
        if (txBitrateEl) txBitrateEl.textContent = '0.0 kbps';
        audioStats.initialized = true;
    }
    
    if (audioStats.transmitting) {
        audioStats.transmissionDuration = (now - audioStats.transmissionStartTime) / 1000;
        
        // Simple TX bitrate calculation with last-valid-value fallback
        let calculatedTxBitrate = 0;
        if (audioStats.transmissionDuration > 0.1 && audioStats.transmittedBytes > 0) {
            calculatedTxBitrate = (audioStats.transmittedBytes * 8) / (audioStats.transmissionDuration * 1000);
            
            // If result is valid, use it and store as last valid value
            if (isFinite(calculatedTxBitrate) && calculatedTxBitrate >= 0) {
                audioStats.lastValidTxBitrate = calculatedTxBitrate;
                audioStats.currentTransmitBitrate = calculatedTxBitrate;
            } else {
                // Use last valid value if current calculation is invalid
                audioStats.currentTransmitBitrate = audioStats.lastValidTxBitrate;
            }
        } else {
            // Use last valid value when no data yet
            audioStats.currentTransmitBitrate = audioStats.lastValidTxBitrate;
        }
        
        // Simple TX display updates using last valid values
        const safeTxDuration = isFinite(audioStats.transmissionDuration) ? audioStats.transmissionDuration.toFixed(1) : '0.0';
        const safeTxBitrate = isFinite(audioStats.currentTransmitBitrate) ? audioStats.currentTransmitBitrate.toFixed(1) : '0.0';
        
        document.getElementById('tx-duration').textContent = safeTxDuration + 's';
        document.getElementById('tx-bytes').textContent = formatBytes(audioStats.transmittedBytes || 0);
        document.getElementById('tx-bitrate').textContent = safeTxBitrate + ' kbps';
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
            document.getElementById('rx-bitrate').textContent = '0.0 kbps';
            return;
        }
        
        audioStats.receptionDuration = (now - audioStats.receptionStartTime) / 1000;
        
        // More robust bitrate calculation with extensive validation
        let calculatedBitrate = 0;
        
        // Validate all input values
        const duration = audioStats.receptionDuration;
        const bytes = audioStats.receivedBytes;
        
        // Simple bitrate calculation with last-valid-value fallback
        if (duration > 0.1 && bytes > 0) {
            calculatedBitrate = (bytes * 8) / (duration * 1000);
            
            // If result is valid, use it and store as last valid value
            if (isFinite(calculatedBitrate) && calculatedBitrate >= 0) {
                audioStats.lastValidRxBitrate = calculatedBitrate;
                audioStats.currentReceiveBitrate = calculatedBitrate;
            } else {
                // Use last valid value if current calculation is invalid
                audioStats.currentReceiveBitrate = audioStats.lastValidRxBitrate;
            }
        } else {
            // Use last valid value when no data yet
            audioStats.currentReceiveBitrate = audioStats.lastValidRxBitrate;
        }
        
        // Simple display updates using last valid values
        const safeDuration = isFinite(audioStats.receptionDuration) ? audioStats.receptionDuration.toFixed(1) : '0.0';
        const safeBitrate = isFinite(audioStats.currentReceiveBitrate) ? audioStats.currentReceiveBitrate.toFixed(1) : '0.0';
        
        document.getElementById('rx-duration').textContent = safeDuration + 's';
        document.getElementById('rx-bytes').textContent = formatBytes(audioStats.receivedBytes || 0);
        document.getElementById('rx-bitrate').textContent = safeBitrate + ' kbps';
        document.getElementById('rx-source').textContent = audioStats.currentReceivingSource || 'Unknown';
    }
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
        webCodecsDecoder = new AudioDecoder({
            output: (audioData) => {
                playDecodedAudio(audioData);
            },
            error: (err) => {
                addMessage('❌ Decoder error: ' + err.message);
            }
        });
        
        webCodecsDecoder.configure({
            codec: 'opus',
            sampleRate: OPUS_SAMPLE_RATE,
            numberOfChannels: 1
        });
        
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
        
        // Create array buffer from encoded chunk
        const buffer = new ArrayBuffer(encodedChunk.byteLength);
        encodedChunk.copyTo(new Uint8Array(buffer));
        ws.send(buffer);
        
        // Update transmission stats
        audioStats.transmittedBytes += byteLength;
        
        if (Math.random() < 0.05) { // Only log 5% of chunks
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
        addMessage('🎚️ Audio mixer initialized');
    }
}

function addToAudioBuffer(pcmData, sampleRate, userID) {
    // Add audio chunk to buffer
    audioBuffer.push({
        data: pcmData,
        sampleRate: sampleRate,
        userID: userID,
        timestamp: performance.now()
    });

    // Limit buffer size to prevent memory buildup
    if (audioBuffer.length > BUFFER_MAX_SIZE) {
        audioBuffer.shift(); // Remove oldest chunk
        addMessage('⚠️ Audio buffer overflow, dropping old chunks');
    }

    // Start playback when we have enough buffered
    if (!isPlayingBuffer && audioBuffer.length >= BUFFER_TARGET_SIZE) {
        startBufferedPlayback();
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
        
        // Use more efficient buffer allocation
        const bufferSize = numberOfFrames * numberOfChannels;
        const pcmBuffer = new ArrayBuffer(bufferSize * 4); // 4 bytes per float32
        const pcmData = new Float32Array(pcmBuffer);
        
        // Copy all audio data efficiently
        audioData.copyTo(pcmBuffer, { planeIndex: 0, format: 'f32-planar' });
        
        // Extract first channel if multi-channel
        let monoData = pcmData;
        if (numberOfChannels > 1) {
            monoData = new Float32Array(numberOfFrames);
            for (let i = 0; i < numberOfFrames; i++) {
                monoData[i] = pcmData[i * numberOfChannels]; // Take first channel
            }
        }
        
        // Play directly for low latency
        await playAudioWithMixing(monoData, sampleRate, 'opus-user');
        
        // Update audio stats with more detailed info
        if (audioData.duration > 0) {
            const bitrate = (audioData.byteLength * 8) / (audioData.duration / 1000000) / 1000;
            document.getElementById('rx-bitrate').textContent = `${bitrate.toFixed(1)} kbps`;
        }
        const channelText = numberOfChannels === 1 ? 'Mono' : `${numberOfChannels}-channel`;
        document.getElementById('rx-channels').textContent = `${channelText} @ ${sampleRate}Hz`;

        // Important: close AudioData to free memory
        audioData.close();
        
    } catch (error) {
        addMessage(`❌ Error playing decoded audio: ${error.message}`);
        if (audioData) {
            audioData.close(); // Ensure cleanup even on error
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

        if (!pcmData || pcmData.length === 0) return;

        // Create audio buffer
        const audioBuffer = audioContext.createBuffer(1, pcmData.length, sampleRate);
        audioBuffer.getChannelData(0).set(pcmData);

        const userGain = audioContext.createGain();
        userGain.gain.value = 0.6; // Conservative volume for buffered playback

        const source = audioContext.createBufferSource();
        source.buffer = audioBuffer;
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

        if (!pcmData || pcmData.length === 0) return;

        // Add to buffer for smooth playback
        audioBuffer.push({ pcmData, sampleRate, userID });
        
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
            
            const { pcmData, sampleRate, userID } = audioBuffer.shift();
            
            const audioBufferNode = audioContext.createBuffer(1, pcmData.length, sampleRate);
            audioBufferNode.getChannelData(0).set(pcmData);

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
            
            // Update next play time based on audio duration (20ms for Opus frames)
            const frameDuration = pcmData.length / sampleRate;
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
        if (!metadata) {
            addMessage('⚠️ Received audio data without metadata. Cannot play.');
            return;
        }

        let pcmData;
        const format = metadata.data.format;
        const sampleRate = metadata.data.sample_rate;
        
        // Start reception stats if not already started
        if (!audioStats.receiving) {
            // Try to get username from various places in metadata
            const sourceName = (metadata.data && metadata.data.username) || 
                              (currentAudioMetadata && currentAudioMetadata.data && currentAudioMetadata.data.username) ||
                              userID;
            audioStats.currentReceivingSource = sourceName;
            startReceptionStats();
        }
        
        // Update reception stats
        audioStats.receivedBytes += audioData.byteLength || audioData.length || 0;
        audioStats.lastAudioReceived = performance.now();

        if (format !== 'opus') {
            addMessage(`⚠️ Non-Opus audio format "${format}" from ${userID}. Only Opus is supported.`);
            return;
        }
        
        if (!webCodecsSupported) {
            addMessage(`⚠️ WebCodecs not supported, skipping audio from ${userID}`);
            return;
        }

        // Ensure decoder is ready and recreate if needed
        if (!webCodecsDecoder || webCodecsDecoder.state === 'closed' || webCodecsDecoder.state === 'unconfigured') {
            try {
                webCodecsDecoder = new AudioDecoder({
                    output: (audioData) => {
                        playDecodedAudio(audioData);
                    },
                    error: (err) => {
                        addMessage(`❌ Decoder error from ${userID}: ${err.message}`);
                        // Reset decoder on error for next attempt
                        webCodecsDecoder = null;
                    }
                });
                
                // Configure decoder for Opus without description (raw Opus packets)
                webCodecsDecoder.configure({
                    codec: 'opus',
                    sampleRate: OPUS_SAMPLE_RATE,
                    numberOfChannels: 1
                });
                
                addMessage(`🔧 Recreated WebCodecs decoder for ${userID}`);
                // Reset timestamp and playback timing for new decoder instance
                decoderTimestamp = 0;
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
            const encodedChunk = new EncodedAudioChunk({
                type: 'key', // Required property for Opus frames
                timestamp: decoderTimestamp, // Use incrementing timestamp
                data: audioData,
                duration: estimatedDuration
            });
            
            // Increment timestamp for next chunk based on estimated duration
            decoderTimestamp += estimatedDuration;
            
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
    
    ws.onopen = function() {
        connectionStatus.classList.add('connected');
        connectionText.textContent = 'Connected';
        addMessage('Connected to server');
        updateStats();
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
            
        case 'error':
            // Handle server-side errors with user-friendly messages
            const errorMsg = event.data.message || 'Unknown server error';
            const errorCode = event.data.code || 'UNKNOWN';
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
            document.getElementById('active-channels').textContent = stats.active_channels;
            document.getElementById('connected-users').textContent = stats.active_users;
            document.getElementById('total-clients').textContent = stats.connected_clients;
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
                    
                    return `<div class="channel-card ${isCurrentChannel ? 'active' : ''} ${isTalking ? 'talking' : ''} ${publishOnlyClass}" 
                                 id="channel-card-${channel.id}"
                                 data-publish-only="${isPublishOnly}"
                                 onclick="selectChannel('${channel.id}')">
                                <div class="channel-status ${isCurrentChannel ? 'active' : ''} ${isTalking ? 'talking' : ''}"></div>
                                <div class="channel-name">${channel.name}</div>
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