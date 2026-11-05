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

// Never offer system authorization surfaces or another Golder GUI as targets.
// A process without a verifiable application bundle fails closed.
int cu_app_allowed(int32_t pid) {
	@autoreleasepool {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
		NSString *bundle = app.bundleIdentifier.lowercaseString;
		if (!bundle || bundle.length == 0) return 0;
		NSArray *denied = @[@"dev.golder.gui", @"com.apple.systempreferences", @"com.apple.securityagent", @"com.apple.loginwindow", @"com.apple.systemuiserver", @"com.apple.coreservicesuiagent"];
		for (NSString *prefix in denied) {
			if ([bundle isEqualToString:prefix] || [bundle hasPrefix:[prefix stringByAppendingString:@"."]]) return 0;
		}
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

static BOOL cu_wait(GolderCaptureResult *result) {
	NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:5.0];
	[result->condition lock];
	while (!result->done && [result->condition waitUntilDate:deadline]) {}
	BOOL done = result->done;
	[result->condition unlock];
	return done;
}

// Return a PNG of exactly the selected SCWindow. No permission prompts are
// initiated here; the operator must grant the OS permission separately.
int cu_capture_window(uint32_t pid, uint32_t windowID, void **out, size_t *length) {
	@autoreleasepool {
		*out = NULL;
		*length = 0;
		if (!CGPreflightScreenCaptureAccess()) return 0;
		if (@available(macOS 14.0, *)) {
			GolderCaptureResult *inventory = [[GolderCaptureResult alloc] init];
			[SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:YES completionHandler:^(SCShareableContent *content, NSError *error) {
				[inventory->condition lock];
				if (!error && content) inventory->content = [content retain];
				inventory->done = YES;
				[inventory->condition signal];
				[inventory->condition unlock];
			}];
			if (!cu_wait(inventory) || !inventory->content) { [inventory release]; return 0; }
			SCWindow *selected = nil;
			int matches = 0;
			for (SCWindow *window in inventory->content.windows) {
				if (window.windowID == windowID && window.owningApplication.processID == (pid_t)pid) {
					selected = window;
					matches++;
				}
			}
			if (matches != 1 || !selected || selected.frame.size.width < 1 || selected.frame.size.height < 1) { [inventory release]; return 0; }
			SCContentFilter *filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:selected];
			SCStreamConfiguration *configuration = [[SCStreamConfiguration alloc] init];
			double width = selected.frame.size.width, height = selected.frame.size.height;
			double scale = fmin(1.0, fmin(1600.0/width, 1200.0/height));
			configuration.width = (size_t)fmax(1, ceil(width*scale));
			configuration.height = (size_t)fmax(1, ceil(height*scale));
			configuration.showsCursor = NO;
			GolderCaptureResult *shot = [[GolderCaptureResult alloc] init];
			[SCScreenshotManager captureImageWithFilter:filter configuration:configuration completionHandler:^(CGImageRef image, NSError *error) {
				[shot->condition lock];
				if (!error && image) shot->image = CGImageRetain(image);
				shot->done = YES;
				[shot->condition signal];
				[shot->condition unlock];
			}];
			[filter release];
			[configuration release];
			[inventory release];
			if (!cu_wait(shot) || !shot->image) { [shot release]; return 0; }
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
			return 1;
		}
		return 0;
	}
}
