package provider

// A driver knows the wire it speaks. Reporting it matters for providers whose
// declared wire list has a single entry: withWireFallback adds no wrapper there
// (there is nothing to fall back to), so without these methods the status bar
// fell back to the stream-retry wrapper's initial guess — Chat — even while the
// driver was talking the Responses API.

func (d *responsesDriver) CurrentWire() Wire { return WireResponses }

func (d *openAICompatDriver) CurrentWire() Wire { return WireChat }

func (d *anthropicCompatDriver) CurrentWire() Wire { return WireMessages }

func (d *geminiCompatDriver) CurrentWire() Wire { return WireGemini }
