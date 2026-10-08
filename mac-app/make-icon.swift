import AppKit
import CoreGraphics

guard CommandLine.arguments.count == 2 else { exit(2) }
let output = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
let sage = NSColor(calibratedRed: 0.157, green: 0.388, blue: 0.306, alpha: 1)
let cream = NSColor(calibratedRed: 0.969, green: 0.957, blue: 0.918, alpha: 1)

func drawIcon(pixels: Int) throws -> Data {
    guard let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0),
        let graphics = NSGraphicsContext(bitmapImageRep: bitmap) else { throw NSError(domain: "Icon", code: 1) }
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = graphics
    let context = graphics.cgContext
    context.scaleBy(x: CGFloat(pixels) / 1024, y: CGFloat(pixels) / 1024)
    context.setShouldAntialias(true)
    let tile = NSBezierPath(roundedRect: NSRect(x: 58, y: 58, width: 908, height: 908), xRadius: 210, yRadius: 210)
    context.saveGState()
    let shadow = NSShadow()
    shadow.shadowColor = sage.withAlphaComponent(0.16)
    shadow.shadowBlurRadius = 26
    shadow.shadowOffset = NSSize(width: 0, height: -12)
    shadow.set()
    cream.setFill(); tile.fill()
    context.restoreGState()
    NSGradient(colors: [NSColor.white, cream])!.draw(in: tile, angle: 270)
    let halo = NSBezierPath(ovalIn: NSRect(x: 195, y: 208, width: 634, height: 634))
    sage.withAlphaComponent(0.045).setFill(); halo.fill()
    let bowl = NSBezierPath()
    bowl.move(to: NSPoint(x: 246, y: 501))
    bowl.curve(to: NSPoint(x: 404, y: 294), controlPoint1: NSPoint(x: 268, y: 359), controlPoint2: NSPoint(x: 323, y: 294))
    bowl.line(to: NSPoint(x: 620, y: 294))
    bowl.curve(to: NSPoint(x: 778, y: 501), controlPoint1: NSPoint(x: 701, y: 294), controlPoint2: NSPoint(x: 756, y: 359))
    bowl.close(); sage.setFill(); bowl.fill()
    NSBezierPath(roundedRect: NSRect(x: 382, y: 247, width: 260, height: 35), xRadius: 17, yRadius: 17).fill()
    NSBezierPath(ovalIn: NSRect(x: 245, y: 454, width: 534, height: 105)).fill()
    cream.setFill(); NSBezierPath(ovalIn: NSRect(x: 285, y: 480, width: 454, height: 57)).fill()
    let stem = NSBezierPath()
    stem.move(to: NSPoint(x: 508, y: 521))
    stem.curve(to: NSPoint(x: 560, y: 742), controlPoint1: NSPoint(x: 493, y: 632), controlPoint2: NSPoint(x: 520, y: 677))
    stem.lineWidth = 23; stem.lineCapStyle = .round; sage.setStroke(); stem.stroke()
    let left = NSBezierPath()
    left.move(to: NSPoint(x: 510, y: 616))
    left.curve(to: NSPoint(x: 369, y: 743), controlPoint1: NSPoint(x: 418, y: 610), controlPoint2: NSPoint(x: 362, y: 670))
    left.curve(to: NSPoint(x: 510, y: 616), controlPoint1: NSPoint(x: 465, y: 749), controlPoint2: NSPoint(x: 520, y: 690))
    left.close(); sage.setFill(); left.fill()
    let right = NSBezierPath()
    right.move(to: NSPoint(x: 525, y: 643))
    right.curve(to: NSPoint(x: 691, y: 789), controlPoint1: NSPoint(x: 522, y: 738), controlPoint2: NSPoint(x: 595, y: 794))
    right.curve(to: NSPoint(x: 525, y: 643), controlPoint1: NSPoint(x: 696, y: 695), controlPoint2: NSPoint(x: 628, y: 639))
    right.close(); sage.setFill(); right.fill()
    let glint = NSBezierPath()
    glint.move(to: NSPoint(x: 320, y: 430)); glint.curve(to: NSPoint(x: 395, y: 340), controlPoint1: NSPoint(x: 335, y: 381), controlPoint2: NSPoint(x: 357, y: 352))
    glint.lineWidth = 18; glint.lineCapStyle = .round; cream.withAlphaComponent(0.8).setStroke(); glint.stroke()
    NSGraphicsContext.restoreGraphicsState()
    return bitmap.representation(using: .png, properties: [:])!
}
for points in [16, 32, 128, 256, 512] {
    for scale in [1, 2] {
        let suffix = scale == 2 ? "@2x" : ""
        try drawIcon(pixels: points * scale).write(to: output.appendingPathComponent("icon_\(points)x\(points)\(suffix).png"))
    }
}
