//go:build darwin && cgo

#import <AppKit/AppKit.h>
#include <stdint.h>

extern int golderPauseItemState(uintptr_t, int);

// This status item never orders a window or activates either application.
// NSTimer owns its target until invalidation. No application event monitor.
@interface GolderComputerPauseItem : NSObject {
    uintptr_t key;
    NSStatusItem *item;
}
- (void)install:(uintptr_t)session;
@end

@implementation GolderComputerPauseItem
- (void)pause:(id)sender {
    if (golderPauseItemState(key, 1) == 2) item.button.enabled = NO;
}
- (void)refresh:(NSTimer *)timer {
    int state = golderPauseItemState(key, 0);
    if (!state) {
        item.button.target = nil;
        [[NSStatusBar systemStatusBar] removeStatusItem:item];
        [item release];
        item = nil;
        golderPauseItemState(key, 2);
        [timer invalidate];
        return;
    }
    item.button.enabled = state == 1;
    item.button.title = state == 1 ? @"G ⏸" : @"G · 已暂停";
}
- (void)install:(uintptr_t)session {
    key = session;
    item = [[[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength] retain];
    item.button.title = @"G ⏸";
    item.button.toolTip = @"暂停 Golder 桌面操作 / Pause Golder Computer Use";
    item.button.target = self;
    item.button.action = @selector(pause:);
    NSTimer *timer = [NSTimer timerWithTimeInterval:0.5 target:self selector:@selector(refresh:) userInfo:nil repeats:YES];
    [[NSRunLoop mainRunLoop] addTimer:timer forMode:NSRunLoopCommonModes];
}
@end

void golderInstallPauseItem(uintptr_t key) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (!NSApp || !golderPauseItemState(key, 0)) { golderPauseItemState(key, 2); return; }
        GolderComputerPauseItem *entry = [[GolderComputerPauseItem alloc] init];
        [entry install:key];
        [entry release];
    });
}
