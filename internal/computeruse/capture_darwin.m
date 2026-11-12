//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <ImageIO/ImageIO.h>
#import <CoreGraphics/CoreGraphics.h>
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
