//go:build installer && darwin && cgo

#import <AppKit/AppKit.h>

extern void goInstallerAction(int shell);
extern int goInstallerCanClose(void);
extern void goInstallerTick(void);

@interface CSQLSetupDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate> {
    NSWindow *window;
    NSTextView *logView;
    NSTextField *statusLabel;
    NSPopUpButton *shellChoice;
    NSButton *primaryButton;
    NSButton *closeButton;
    NSProgressIndicator *progress;
    NSTimer *timer;
    int initialShell;
}
- (id)initWithShell:(int)index;
- (void)updateLog:(NSString *)log status:(NSString *)status action:(NSString *)action enabled:(BOOL)enabled active:(BOOL)active busy:(BOOL)busy;
@end

static CSQLSetupDelegate *setupDelegate;

@implementation CSQLSetupDelegate
- (id)initWithShell:(int)index {
    self = [super init];
    if (self) initialShell = index;
    return self;
}
- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    // UIはAppKitのmain threadだけで操作し、導入処理はGo側workerへ渡す。
    NSRect frame = NSMakeRect(0, 0, 820, 620);
    window = [[NSWindow alloc] initWithContentRect:frame
        styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
        backing:NSBackingStoreBuffered defer:NO];
    [window setTitle:@"csql セットアップ"];
    [window setMinSize:NSMakeSize(700, 480)];
    [window setDelegate:self];
    [window setReleasedWhenClosed:NO];
    NSView *content = [window contentView];
    NSTextField *intro = [NSTextField wrappingLabelWithString:@"Cloud SQL接続CLIと不足ツールをユーザー領域へ導入します。\n導入とPATH設定の前に、それぞれ変更内容を確認します。\nGoogleへのログインは、セットアップ完了後に別途行います。"];
    [intro setFrame:NSMakeRect(20, 528, 780, 72)];
    [intro setFont:[NSFont systemFontOfSize:14]];
    [intro setAutoresizingMask:NSViewWidthSizable | NSViewMinYMargin];
    [content addSubview:intro];
    NSTextField *shellLabel = [NSTextField labelWithString:@"対象シェル"];
    [shellLabel setFrame:NSMakeRect(20, 491, 90, 24)];
    [shellLabel setAutoresizingMask:NSViewMinYMargin];
    [content addSubview:shellLabel];
    shellChoice = [[NSPopUpButton alloc] initWithFrame:NSMakeRect(110, 488, 180, 28) pullsDown:NO];
    [shellChoice addItemsWithTitles:@[@"zsh", @"bash", @"fish"]];
    [shellChoice selectItemAtIndex:initialShell];
    [shellChoice setAutoresizingMask:NSViewMinYMargin];
    [content addSubview:shellChoice];
    NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:NSMakeRect(20, 100, 780, 376)];
    [scroll setBorderType:NSBezelBorder];
    [scroll setHasVerticalScroller:YES];
    [scroll setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable];
    logView = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, 760, 376)];
    [logView setEditable:NO];
    [logView setSelectable:YES];
    [logView setRichText:NO];
    [logView setFont:[NSFont userFixedPitchFontOfSize:12]];
    [logView setVerticallyResizable:YES];
    [logView setHorizontallyResizable:NO];
    [[logView textContainer] setWidthTracksTextView:YES];
    [logView setAutoresizingMask:NSViewWidthSizable];
    [scroll setDocumentView:logView];
    [content addSubview:scroll];
    statusLabel = [NSTextField wrappingLabelWithString:@"準備ができたら開始してください。"];
    [statusLabel setFrame:NSMakeRect(20, 56, 780, 36)];
    [statusLabel setAutoresizingMask:NSViewWidthSizable | NSViewMaxYMargin];
    [content addSubview:statusLabel];
    progress = [[NSProgressIndicator alloc] initWithFrame:NSMakeRect(20, 23, 20, 20)];
    [progress setStyle:NSProgressIndicatorStyleSpinning];
    [progress setDisplayedWhenStopped:NO];
    [progress setAutoresizingMask:NSViewMaxYMargin];
    [content addSubview:progress];
    closeButton = [NSButton buttonWithTitle:@"閉じる" target:self action:@selector(closeSetup:)];
    [closeButton setFrame:NSMakeRect(530, 16, 110, 32)];
    [closeButton setKeyEquivalent:@"\e"];
    [closeButton setAutoresizingMask:NSViewMinXMargin | NSViewMaxYMargin];
    [content addSubview:closeButton];
    primaryButton = [NSButton buttonWithTitle:@"セットアップ開始" target:self action:@selector(startSetup:)];
    [primaryButton setFrame:NSMakeRect(650, 16, 150, 32)];
    [primaryButton setKeyEquivalent:@"\r"];
    [primaryButton setAutoresizingMask:NSViewMinXMargin | NSViewMaxYMargin];
    [content addSubview:primaryButton];
    NSMenu *menu = [[NSMenu alloc] initWithTitle:@""];
    NSMenuItem *appItem = [[NSMenuItem alloc] initWithTitle:@"" action:nil keyEquivalent:@""];
    [menu addItem:appItem];
    NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"csql セットアップ"];
    [appMenu addItemWithTitle:@"csql セットアップを終了" action:@selector(terminate:) keyEquivalent:@"q"];
    [appItem setSubmenu:appMenu];
    [NSApp setMainMenu:menu];
    [window center];
    [window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
    goInstallerTick();
    timer = [NSTimer scheduledTimerWithTimeInterval:0.15 target:self selector:@selector(refresh:) userInfo:nil repeats:YES];
}
- (void)startSetup:(id)sender { goInstallerAction((int)[shellChoice indexOfSelectedItem]); }
- (void)refresh:(id)sender { goInstallerTick(); }
- (void)closeSetup:(id)sender { [window performClose:sender]; }
- (BOOL)windowShouldClose:(id)sender {
    if (!goInstallerCanClose()) return NO;
    [timer invalidate];
    [NSApp stop:nil];
    // stopをイベント待受から確実に戻す。
    [NSApp postEvent:[NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0] atStart:NO];
    return YES;
}
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender {
    return goInstallerCanClose() ? NSTerminateNow : NSTerminateCancel;
}
- (void)updateLog:(NSString *)log status:(NSString *)status action:(NSString *)action enabled:(BOOL)enabled active:(BOOL)active busy:(BOOL)busy {
    [logView setString:log];
    [logView scrollRangeToVisible:NSMakeRange([[logView string] length], 0)];
    [statusLabel setStringValue:status];
    [primaryButton setTitle:action];
    [primaryButton setEnabled:enabled];
    [shellChoice setEnabled:!active];
    [closeButton setTitle:active ? @"取り消す" : @"閉じる"];
    if (busy) [progress startAnimation:nil]; else [progress stopAnimation:nil];
}
@end

int csqlNativeRun(int shell) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        setupDelegate = [[CSQLSetupDelegate alloc] initWithShell:shell];
        [NSApp setDelegate:setupDelegate];
        [NSApp run];
    }
    return 0;
}
void csqlNativeUpdate(const char *log, const char *status, const char *action, int enabled, int active, int busy) {
    [setupDelegate updateLog:[NSString stringWithUTF8String:log] status:[NSString stringWithUTF8String:status]
        action:[NSString stringWithUTF8String:action] enabled:enabled active:active busy:busy];
}
