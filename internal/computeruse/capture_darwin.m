//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <ImageIO/ImageIO.h>
#import <CoreGraphics/CoreGraphics.h>
#import <CoreImage/CoreImage.h>
#import <CoreVideo/CoreVideo.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <stdatomic.h>
#include <unistd.h>

// Keep system authorization surfaces, Golder, and terminal hosts out of both
// application launch and window selection.
static int cu_bundle_allowed(NSString *bundle) {
	if (!bundle || bundle.length == 0) return 0;
	bundle = bundle.lowercaseString;
	NSArray *denied = @[@"dev.golder.gui", @"com.apple.systempreferences", @"com.apple.securityagent", @"com.apple.loginwindow", @"com.apple.systemuiserver", @"com.apple.coreservicesuiagent", @"com.apple.terminal", @"com.googlecode.iterm2", @"net.kovidgoyal.kitty", @"com.github.wez.wezterm", @"com.mitchellh.ghostty", @"org.alacritty", @"dev.warp"];
	for (NSString *prefix in denied) {
		if ([bundle isEqualToString:prefix] || [bundle hasPrefix:[prefix stringByAppendingString:@"."]]) return 0;
	}
	return 1;
}

int cu_app_allowed(int32_t pid) {
	@autoreleasepool {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
		return cu_bundle_allowed(app.bundleIdentifier);
	}
}

int cu_running_bundle(int32_t pid, char *out, size_t length) {
	@autoreleasepool {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
		NSString *bundle = app.bundleIdentifier;
		NSString *path = app.bundleURL.path.stringByResolvingSymlinksInPath;
		if (!cu_bundle_allowed(bundle) || !path || ![path getCString:out maxLength:length encoding:NSUTF8StringEncoding]) return 0;
		return 1;
	}
}

// Activate only the already running, catalogued process. Do not launch another
// copy, activate all windows, or interpret a model-provided application name.
int cu_activate_running_app(int32_t pid, const char *expectedPath) {
	@autoreleasepool {
		if (pid <= 0 || pid == getpid() || !expectedPath) return 0;
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
		NSString *path = [NSString stringWithUTF8String:expectedPath];
		if (!app || app.terminated || !cu_bundle_allowed(app.bundleIdentifier) ||
			![app.bundleURL.path.stringByResolvingSymlinksInPath isEqualToString:path]) return 0;
		return [app activateWithOptions:0] ? 1 : 0;
	}
}

int cu_application_metadata(const char *path, char *identity, size_t identityLength, char *display, size_t displayLength, char *name, size_t nameLength) {
	@autoreleasepool {
		NSString *appPath = [NSString stringWithUTF8String:path];
		NSBundle *bundle = [NSBundle bundleWithPath:appPath];
		NSString *identifier = bundle.bundleIdentifier;
		if (!cu_bundle_allowed(identifier) || ![identifier getCString:identity maxLength:identityLength encoding:NSUTF8StringEncoding]) return 0;
		NSString *label = [[[NSFileManager defaultManager] displayNameAtPath:appPath] stringByDeletingPathExtension];
		NSString *bundleName = [bundle objectForInfoDictionaryKey:@"CFBundleDisplayName"];
		if (![bundleName isKindOfClass:[NSString class]] || bundleName.length == 0) bundleName = [bundle objectForInfoDictionaryKey:@"CFBundleName"];
		if (![bundleName isKindOfClass:[NSString class]]) bundleName = @"";
		if (!label) label = @"";
		if (![label getCString:display maxLength:displayLength encoding:NSUTF8StringEncoding] || ![bundleName getCString:name maxLength:nameLength encoding:NSUTF8StringEncoding]) return 0;
		return 1;
	}
}

@interface GolderCaptureResult : NSObject {
@public
	NSCondition *condition;
	BOOL done;
	SCShareableContent *content;
	CGImageRef image;
}
@end

// One long-lived ScreenCaptureKit stream per selected window. A one-shot
// SCScreenshotManager call returns the last composited image for a window on
// another desktop; a stream receives the window's surface as the app renders.
@interface GolderWindowStream : NSObject <SCStreamOutput> {
@public
	NSCondition *condition;
	SCStream *stream;
	dispatch_queue_t queue;
	NSData *png;
	NSTimeInterval capturedAt;
	int frameStatus;
	CGRect frame;
	BOOL started;
	int32_t pid;
	uint32_t windowID;
}
- (instancetype)initWithPID:(int32_t)pid window:(uint32_t)windowID;
- (void)start;
- (void)stop;
- (BOOL)copyLatest:(void **)out length:(size_t *)length at:(double *)at status:(int *)status frame:(CGRect *)frameOut timeout:(NSTimeInterval)timeout cancelled:(atomic_int *)cancelled;
@end

static NSMutableDictionary *cu_streams;
static NSLock *cu_streams_lock;

@implementation GolderWindowStream
- (instancetype)initWithPID:(int32_t)process window:(uint32_t)window {
	self = [super init];
	if (self) {
		condition = [[NSCondition alloc] init];
		queue = dispatch_queue_create("dev.golder.capture.stream", DISPATCH_QUEUE_SERIAL);
		pid = process;
		windowID = window;
	}
	return self;
}
- (void)dealloc {
	[condition release];
	[png release];
	if (stream) [stream release];
	if (queue) dispatch_release(queue);
	[super dealloc];
}
- (void)start {
	@synchronized(self) {
		if (started) return;
		started = YES;
	}
	[SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:NO completionHandler:^(SCShareableContent *content, NSError *error) {
		if (error || !content) return;
		SCWindow *selected = nil;
		for (SCWindow *window in content.windows) {
			if (window.windowID == self->windowID && window.owningApplication.processID == self->pid) { selected = window; break; }
		}
		if (!selected) return;
		SCContentFilter *filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:selected];
		SCStreamConfiguration *configuration = [[SCStreamConfiguration alloc] init];
		double width = selected.frame.size.width, height = selected.frame.size.height;
		double scale = fmin(1.0, fmin(1600.0/width, 1200.0/height));
		configuration.width = (size_t)fmax(1, ceil(width*scale));
		configuration.height = (size_t)fmax(1, ceil(height*scale));
		configuration.showsCursor = NO;
		configuration.capturesAudio = NO;
		configuration.queueDepth = 3;
		configuration.minimumFrameInterval = CMTimeMake(1, 15);
		SCStream *created = [[SCStream alloc] initWithFilter:filter configuration:configuration delegate:nil];
		[created addStreamOutput:self type:SCStreamOutputTypeScreen sampleHandlerQueue:self->queue error:nil];
		[filter release];
		[configuration release];
		@synchronized(self) {
			self->stream = created;
			self->frame = selected.frame;
		}
		[created startCaptureWithCompletionHandler:^(NSError *startError) {}];
	}];
}
- (void)stop {
	@synchronized(self) {
		if (stream) { [stream stopCaptureWithCompletionHandler:nil]; [stream release]; stream = nil; }
	}
}
- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type {
	if (type != SCStreamOutputTypeScreen) return;
	int status = 0;
	CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, false);
	if (attachments && CFArrayGetCount(attachments) > 0) {
		NSDictionary *dict = (__bridge NSDictionary *)CFArrayGetValueAtIndex(attachments, 0);
		NSNumber *value = dict[(id)SCStreamFrameInfoStatus];
		if (value) status = value.intValue;
	}
	CVPixelBufferRef buffer = CMSampleBufferGetImageBuffer(sampleBuffer);
	if (!buffer) return;
	CIImage *image = [CIImage imageWithCVPixelBuffer:buffer];
	static CIContext *context;
	if (!context) context = [[CIContext alloc] init];
	CGImageRef cg = [context createCGImage:image fromRect:image.extent];
	if (!cg) return;
	NSMutableData *data = [NSMutableData data];
	CGImageDestinationRef destination = CGImageDestinationCreateWithData((CFMutableDataRef)data, CFSTR("public.png"), 1, NULL);
	if (destination) {
		CGImageDestinationAddImage(destination, cg, NULL);
		CGImageDestinationFinalize(destination);
		CFRelease(destination);
	}
	CGImageRelease(cg);
	if (data.length == 0 || data.length > 8*1024*1024) return;
	[condition lock];
	[png release];
	png = [data copy];
	capturedAt = [NSDate timeIntervalSinceReferenceDate];
	frameStatus = status;
	[condition broadcast];
	[condition unlock];
}
- (BOOL)copyLatest:(void **)out length:(size_t *)length at:(double *)at status:(int *)statusOut frame:(CGRect *)frameOut timeout:(NSTimeInterval)timeout cancelled:(atomic_int *)cancelled {
	NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:timeout];
	[condition lock];
	while (!png && !atomic_load(cancelled) && deadline.timeIntervalSinceNow > 0) {
		[condition waitUntilDate:[NSDate dateWithTimeIntervalSinceNow:fmin(0.05, deadline.timeIntervalSinceNow)]];
	}
	BOOL ok = png != nil && !atomic_load(cancelled);
	if (ok) {
		*out = malloc(png.length);
		if (*out) {
			memcpy(*out, png.bytes, png.length);
			*length = png.length;
			*at = capturedAt;
			*statusOut = frameStatus;
			*frameOut = self->frame;
		} else {
			ok = NO;
		}
	}
	[condition unlock];
	return ok;
}
@end

static GolderWindowStream *cu_stream_for(int32_t pid, uint32_t windowID) {
	static dispatch_once_t once;
	dispatch_once(&once, ^{
		cu_streams = [[NSMutableDictionary alloc] init];
		cu_streams_lock = [[NSLock alloc] init];
	});
	NSString *key = [NSString stringWithFormat:@"%d:%u", pid, windowID];
	[cu_streams_lock lock];
	GolderWindowStream *stream = [cu_streams objectForKey:key];
	if (!stream) {
		stream = [[[GolderWindowStream alloc] initWithPID:pid window:windowID] autorelease];
		[cu_streams setObject:stream forKey:key];
	}
	[cu_streams_lock unlock];
	[stream start];
	return stream;
}

int cu_stream_latest(int32_t pid, uint32_t windowID, double timeout, void **out, size_t *length, double *at, int *status, double *x, double *y, double *w, double *h, void *cancel) {
	GolderWindowStream *stream = cu_stream_for(pid, windowID);
	CGRect frame = CGRectZero;
	if (![stream copyLatest:out length:length at:at status:status frame:&frame timeout:timeout cancelled:(atomic_int *)cancel]) return 0;
	*x = frame.origin.x;
	*y = frame.origin.y;
	*w = frame.size.width;
	*h = frame.size.height;
	return 1;
}

void cu_stream_stop_all(void) {
	if (!cu_streams) return;
	[cu_streams_lock lock];
	NSArray *all = [cu_streams allValues];
	[cu_streams removeAllObjects];
	[cu_streams_lock unlock];
	for (GolderWindowStream *stream in all) [stream stop];
}

@implementation GolderCaptureResult
- (instancetype)init {
	self = [super init];
	if (self) condition = [[NSCondition alloc] init];
	return self;
}
- (void)dealloc {
	[condition release];
	[content release];
	if (image) CGImageRelease(image);
	[super dealloc];
}
@end

static BOOL cu_wait(GolderCaptureResult *result, atomic_int *cancelled) {
	NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:5.0];
	[result->condition lock];
	while (!result->done && !atomic_load(cancelled) && deadline.timeIntervalSinceNow > 0) {
		NSDate *next = [NSDate dateWithTimeIntervalSinceNow:fmin(0.05, deadline.timeIntervalSinceNow)];
		[result->condition waitUntilDate:next];
	}
	BOOL done = result->done && !atomic_load(cancelled);
	[result->condition unlock];
	return done;
}

// Return a PNG of exactly the selected SCWindow. No permission prompts are
// initiated here; the operator must grant the OS permission separately.
int cu_capture_window(uint32_t pid, uint32_t windowID, void **out, size_t *length, double *x, double *y, double *w, double *h, void *cancel) {
	@autoreleasepool {
		*out = NULL;
		*length = 0;
		atomic_int *cancelled = (atomic_int *)cancel;
		if (!cancelled || atomic_load(cancelled)) return 0;
		if (!CGPreflightScreenCaptureAccess()) return 0;
		if (@available(macOS 14.0, *)) {
			GolderCaptureResult *inventory = [[GolderCaptureResult alloc] init];
			[inventory retain]; // The completion may arrive after a canceled wait.
			[SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:NO completionHandler:^(SCShareableContent *content, NSError *error) {
				[inventory->condition lock];
				if (!error && content) inventory->content = [content retain];
				inventory->done = YES;
				[inventory->condition signal];
				[inventory->condition unlock];
				[inventory release];
			}];
			if (!cu_wait(inventory, cancelled) || !inventory->content) { [inventory release]; return 0; }
			SCWindow *selected = nil;
			int matches = 0;
			for (SCWindow *window in inventory->content.windows) {
				if (window.windowID == windowID && window.owningApplication.processID == (pid_t)pid) {
					selected = window;
					matches++;
				}
			}
			if (matches != 1 || !selected || selected.frame.size.width < 1 || selected.frame.size.height < 1) { [inventory release]; return 0; }
			CGRect capturedFrame = selected.frame;
			SCContentFilter *filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:selected];
			SCStreamConfiguration *configuration = [[SCStreamConfiguration alloc] init];
			double width = selected.frame.size.width, height = selected.frame.size.height;
			double scale = fmin(1.0, fmin(1600.0/width, 1200.0/height));
			configuration.width = (size_t)fmax(1, ceil(width*scale));
			configuration.height = (size_t)fmax(1, ceil(height*scale));
			configuration.showsCursor = NO;
			configuration.capturesAudio = NO;
			GolderCaptureResult *shot = [[GolderCaptureResult alloc] init];
			[shot retain]; // The completion may arrive after a canceled wait.
			[SCScreenshotManager captureImageWithFilter:filter configuration:configuration completionHandler:^(CGImageRef image, NSError *error) {
				[shot->condition lock];
				if (!error && image) shot->image = CGImageRetain(image);
				shot->done = YES;
				[shot->condition signal];
				[shot->condition unlock];
				[shot release];
			}];
			[filter release];
			[configuration release];
			[inventory release];
			if (!cu_wait(shot, cancelled) || !shot->image) { [shot release]; return 0; }
			NSMutableData *png = [NSMutableData data];
			CGImageDestinationRef destination = CGImageDestinationCreateWithData((CFMutableDataRef)png, CFSTR("public.png"), 1, NULL);
			if (!destination) { [shot release]; return 0; }
			CGImageDestinationAddImage(destination, shot->image, NULL);
			BOOL ok = CGImageDestinationFinalize(destination);
			CFRelease(destination);
			[shot release];
			if (!ok || png.length == 0 || png.length > 8*1024*1024) return 0;
			void *copy = malloc(png.length);
			if (!copy) return 0;
			memcpy(copy, png.bytes, png.length);
			*out = copy;
			*length = png.length;
			*x = capturedFrame.origin.x;
			*y = capturedFrame.origin.y;
			*w = capturedFrame.size.width;
			*h = capturedFrame.size.height;
			return 1;
		}
		return 0;
	}
}
