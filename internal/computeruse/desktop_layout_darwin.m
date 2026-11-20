//go:build darwin && cgo

#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#include <stdint.h>

extern int64_t golderDesktopChoice(uintptr_t, int, int64_t);
typedef int (*LayoutCheck)(const void *, AXUIElementRef, int);
static NSString *const preference = @"GolderComputerWindowedLayout";

static BOOL geometry(AXUIElementRef w, CGRect *r) {
    CFTypeRef p = NULL, s = NULL;
    BOOL ok = AXUIElementCopyAttributeValue(w,kAXPositionAttribute,&p)==kAXErrorSuccess &&
        AXUIElementCopyAttributeValue(w,kAXSizeAttribute,&s)==kAXErrorSuccess && p && s &&
        CFGetTypeID(p)==AXValueGetTypeID() && CFGetTypeID(s)==AXValueGetTypeID() &&
        AXValueGetValue(p,kAXValueCGPointType,&r->origin) && AXValueGetValue(s,kAXValueCGSizeType,&r->size) &&
        !CGRectIsEmpty(*r) && !CGRectIsInfinite(*r);
    if (p) CFRelease(p);
    if (s) CFRelease(s);
    return ok;
}

static BOOL attributeFalse(AXUIElementRef w, CFStringRef name) {
    CFTypeRef v = NULL;
    BOOL ok = AXUIElementCopyAttributeValue(w,name,&v)==kAXErrorSuccess && v &&
        CFGetTypeID(v)==CFBooleanGetTypeID() && !CFBooleanGetValue(v);
    if (v) CFRelease(v);
    return ok;
}

static BOOL writeGeometry(AXUIElementRef w, BOOL position, CGRect r) {
    AXValueRef v = position ? AXValueCreate(kAXValueCGPointType,&r.origin) : AXValueCreate(kAXValueCGSizeType,&r.size);
    BOOL ok = v && AXUIElementSetAttributeValue(w,position?kAXPositionAttribute:kAXSizeAttribute,v)==kAXErrorSuccess;
    if (v) CFRelease(v);
    return ok;
}

// All state below is confined to AppKit's main run loop. Keep independent
// restoration receipts for the two windows. Never search for a replacement.
@interface GolderDesktopLayout : NSObject {
@public
    uintptr_t key;
    NSWindow *host;
    AXUIElementRef target;
    NSData *identity;
    LayoutCheck check;
    int64_t ticket;
    NSAlert *sheet;
    BOOL awaitingExit, exited, hostOwned, targetOwned, minOwned, ignorePreference;
    NSTimeInterval exitDeadline;
    NSRect hostBefore, hostApplied, exitFrame;
    NSSize minBefore;
    CGRect targetBefore, targetApplied;
}
- (void)tick:(NSTimer *)timer;
@end

// Prevent multiple conversations from competing over the same host layout.
static GolderDesktopLayout *owner;

@implementation GolderDesktopLayout
- (BOOL)live { return key && ticket>0 && golderDesktopChoice(key,0,0)==ticket; }
- (BOOL)normalTarget {
    return check(identity.bytes,target,0) && attributeFalse(target,kAXMinimizedAttribute) && attributeFalse(target,CFSTR("AXFullScreen"));
}
- (void)restore {
    if (owner != self) return;
    BOOL hostSafe = host.isVisible && host.isOnActiveSpace && !host.isMiniaturized && !(host.styleMask & NSWindowStyleMaskFullScreen);
    BOOL unchanged = hostOwned && NSEqualRects(host.frame,hostApplied);
    if (targetOwned && [self normalTarget]) {
        CGRect r;
        if (geometry(target,&r) && CGRectEqualToRect(r,targetApplied) && writeGeometry(target,YES,targetBefore)) {
            CGRect intermediate = CGRectMake(targetBefore.origin.x,targetBefore.origin.y,targetApplied.size.width,targetApplied.size.height);
            if ([self normalTarget] && geometry(target,&r) && CGRectEqualToRect(r,intermediate)) writeGeometry(target,NO,targetBefore);
        }
    }
    if (hostSafe && unchanged) [host setFrame:hostBefore display:YES];
    if (minOwned && NSEqualSizes(host.minSize,NSMakeSize(280,220))) host.minSize=minBefore;
    // Fullscreen restoration is allowed only for our own window still in the
    // exact post-exit layout. Never activate an app or select a Space here.
    if (exited && hostSafe && NSEqualRects(host.frame,exitFrame) && NSApp.isActive) [host toggleFullScreen:nil];
    owner=nil;
    hostOwned=targetOwned=minOwned=exited=NO;
}
- (void)failure:(NSString *)why {
    [self restore];
    ignorePreference=YES;
    // End the pending request first: a failed tiling attempt must never leave
    // the request open, or the tick timer reopens the sheet. The alert below
    // is informational only.
    BOOL wasLive = [self live];
    if (wasLive) golderDesktopChoice(key,1,ticket);
    if (!wasLive || host.attachedSheet || !host.isVisible || !host.isOnActiveSpace || host.isMiniaturized) return;
    NSAlert *a = [[[NSAlert alloc] init] autorelease];
    a.messageText=@"无法并排 / Tiling unavailable";
    a.informativeText=why;
    [a addButtonWithTitle:@"好 / OK"];
    [a beginSheetModalForWindow:host completionHandler:nil];
}
- (void)tile {
    if (![self live]) return;
    NSScreen *screen=host.screen;
    NSRect work=screen.visibleFrame;
    if (owner && owner!=self) {
        [self failure:@"另一个会话正在并排操作；本会话保持布局，不会争用窗口。"];
        return;
    }
    if (!screen || !host.isOnActiveSpace || host.isMiniaturized || (host.styleMask & NSWindowStyleMaskFullScreen) ||
        work.size.width<1000 || work.size.height<560) {
        [self failure:@"当前桌面或屏幕空间不适合并排。保持布局，在菜单栏用 G ⏸ 暂停。"];
        return;
    }
    // AX global coordinates have their origin at the top of the primary
    // display; AppKit coordinates grow upwards (including secondary screens).
    CGFloat top=NSMaxY([NSScreen screens].firstObject.frame);
    CGRect display=CGRectMake(NSMinX(screen.frame),top-NSMaxY(screen.frame),NSWidth(screen.frame),NSHeight(screen.frame));
    Boolean canMove=false, canSize=false;
    if (![self normalTarget] || !geometry(target,&targetBefore) || !CGRectContainsRect(display,targetBefore) ||
        AXUIElementIsAttributeSettable(target,kAXPositionAttribute,&canMove)!=kAXErrorSuccess || !canMove ||
        AXUIElementIsAttributeSettable(target,kAXSizeAttribute,&canSize)!=kAXErrorSuccess || !canSize) {
        [self failure:@"不能确认目标为同一显示器、当前桌面上可移动且可缩放的普通窗口。请先还原目标，或保持布局。"];
        return;
    }
    owner=self;
    hostBefore=host.frame;
    CGFloat sidebar=MIN(380,MAX(300,floor(work.size.width*.28)));
    NSRect narrow=NSMakeRect(work.origin.x,work.origin.y,sidebar,work.size.height);
    CGRect wide=CGRectMake(NSMinX(work)+sidebar+8,top-NSMaxY(work),work.size.width-sidebar-8,work.size.height);
    // Resize then move, verifying each exact applied geometry. On partial
    // success only the verified intermediate change is eligible for rollback.
    CGRect observed;
    CGRect intermediate=CGRectMake(targetBefore.origin.x,targetBefore.origin.y,wide.size.width,wide.size.height);
    if (![self live] || ![self normalTarget] || !geometry(target,&observed) || !CGRectEqualToRect(observed,targetBefore) ||
        !writeGeometry(target,NO,wide) || !geometry(target,&observed) || !CGRectEqualToRect(observed,intermediate)) {
        [self failure:@"目标拒绝尺寸或未能确认变更；没有继续移动窗口。"];
        return;
    }
    targetApplied=intermediate; targetOwned=YES;
    if (![self live] || ![self normalTarget] || !geometry(target,&observed) || !CGRectEqualToRect(observed,intermediate) ||
        !writeGeometry(target,YES,wide) || !geometry(target,&observed) || !CGRectEqualToRect(observed,wide)) {
        [self failure:@"未能确认目标位置；未发送输入。"];
        return;
    }
    targetApplied=wide;
    if (![self live] || !NSEqualRects(host.frame,hostBefore)) { [self restore]; return; }
    minBefore=host.minSize; host.minSize=NSMakeSize(280,220); minOwned=YES;
    [host setFrame:narrow display:YES]; hostApplied=host.frame; hostOwned=YES;
    if (!NSEqualRects(hostApplied,narrow) || !golderDesktopChoice(key,2,ticket)) { [self restore]; return; }
}
- (void)didExit:(NSNotification *)unused {
    if (!awaitingExit) return;
    awaitingExit=NO; exited=YES; exitFrame=host.frame;
    if ([self live]) [self tile]; else [self restore];
}
- (void)ask {
    BOOL full=(host.styleMask & NSWindowStyleMaskFullScreen)!=0;
    NSString *saved=[[NSUserDefaults standardUserDefaults] stringForKey:preference];
    if (!full && !ignorePreference && !([NSEvent modifierFlags] & NSEventModifierFlagOption)) {
        if ([saved isEqual:@"keep"]) { golderDesktopChoice(key,1,ticket); return; }
        if ([saved isEqual:@"tile"]) { [self tile]; return; }
    }
    sheet=[[NSAlert alloc] init];
    sheet.messageText=@"第一次前台输入 / Foreground input";
    sheet.informativeText=full ? @"未发送输入。可临时退出全屏并排，或保留 Golder 全屏，仅切到已授权窗口一次。菜单栏 G ⏸ 可暂停。" : @"未发送输入。保持布局不会移动窗口；并排会将 Golder 收成左侧窄栏。停止后只恢复未被手动调整的布局。";
    [sheet addButtonWithTitle:full?@"保留全屏，转到目标":@"保持布局"];
    [sheet addButtonWithTitle:full?@"临时退出全屏并排":@"并排操作"];
    [sheet addButtonWithTitle:@"暂停"];
    if (!full) { sheet.showsSuppressionButton=YES; sheet.suppressionButton.title=@"记住此选择（按住 Option 重新选择）"; }
    [sheet beginSheetModalForWindow:host completionHandler:^(NSModalResponse r){
        BOOL remember=!full && sheet.suppressionButton.state==NSControlStateValueOn;
        [sheet release]; sheet=nil;
        if (![self live]) return;
        if (r==NSAlertThirdButtonReturn) { golderDesktopChoice(key,4,ticket); return; }
        if (r!=NSAlertSecondButtonReturn && r!=NSAlertFirstButtonReturn) return;
        BOOL split=r==NSAlertSecondButtonReturn;
        if (remember) [[NSUserDefaults standardUserDefaults] setObject:(split?@"tile":@"keep") forKey:preference];
        if (!split) {
            if (full && (!attributeFalse(target,kAXMinimizedAttribute) || !check(identity.bytes,target,2))) {
                [self failure:@"不能唯一、安全地前置此窗口，请手动显示后再操作。"];
            } else { golderDesktopChoice(key,1,ticket); }
        } else if (!full) { [self tile]; }
        else if (!owner && (host.styleMask & NSWindowStyleMaskFullScreen)) {
            owner=self; awaitingExit=YES; exitDeadline=NSProcessInfo.processInfo.systemUptime+8; [host toggleFullScreen:nil];
        } else { [self failure:@"全屏状态已变化，请重新选择布局。 "]; }
    }];
}
- (void)tick:(NSTimer *)timer {
    int64_t state=golderDesktopChoice(key,0,0);
    if (awaitingExit && NSProcessInfo.processInfo.systemUptime>exitDeadline) {
        awaitingExit=NO;
        [self failure:@"未能确认已退出全屏；不会重试或移动目标。"];
    }
    if (state<0) {
        if (sheet && host.attachedSheet==sheet.window) [host endSheet:sheet.window returnCode:NSModalResponseCancel];
        if (awaitingExit) return; // finish the owned transition before releasing the receipt
        [self restore];
        [[NSNotificationCenter defaultCenter] removeObserver:self];
        golderDesktopChoice(key,3,0); key=0;
        [timer invalidate]; return;
    }
    if (owner==self && hostOwned && !NSEqualRects(host.frame,hostApplied)) { hostOwned=NO; exited=NO; }
    if (sheet && state!=ticket && host.attachedSheet==sheet.window) [host endSheet:sheet.window returnCode:NSModalResponseCancel];
    if (!state || sheet || host.attachedSheet || awaitingExit || !host.isVisible || !host.isOnActiveSpace || host.isMiniaturized) return;
    ticket=state; [self ask];
}
- (void)dealloc {
    if (target) CFRelease(target);
    [identity release]; [host release]; [sheet release];
    [super dealloc];
}
@end

void cu_layout_attach(uintptr_t handle, uintptr_t key, AXUIElementRef target, const void *identity, size_t length, LayoutCheck check) {
    NSData *copy=[[NSData alloc] initWithBytes:identity length:length];
    CFRetain(target);
    dispatch_async(dispatch_get_main_queue(), ^{
        NSWindow *host=nil;
        for (NSWindow *w in NSApp.windows) if ((uintptr_t)w==handle) { host=w; break; }
        if (!host) { [copy release]; CFRelease(target); golderDesktopChoice(key,3,0); return; }
        GolderDesktopLayout *layout=[[GolderDesktopLayout alloc] init];
        layout->host=[host retain]; layout->key=key; layout->target=target;
        layout->identity=copy; layout->check=check;
        [[NSNotificationCenter defaultCenter] addObserver:layout selector:@selector(didExit:) name:NSWindowDidExitFullScreenNotification object:host];
        NSTimer *timer=[NSTimer timerWithTimeInterval:0.35 target:layout selector:@selector(tick:) userInfo:nil repeats:YES];
        [[NSRunLoop mainRunLoop] addTimer:timer forMode:NSRunLoopCommonModes];
        [layout release];
    });
}
