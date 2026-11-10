# P6 Hero artwork provenance

2026-10-28。使用内置 imagegen 工具生成独立摄影素材；未裁切或复用用户效果图，未生成 UI 或数据。App资源：`ios/RAI/App/Resources/TrainingHero.jpg`，1280×853 JPEG，由1536×1024生成图等比例缩小并压缩。缓存入口为`HeroArtwork.image`；不联网下载。

生成提示词（原文）：

> Use case: photorealistic-natural. Asset type: background photograph for a native running app training hero card, landscape 3:2. Create an original cinematic realistic photograph of a solitary adult runner seen from behind on the RIGHT third of frame, jogging along a riverside promenade at sunset. Warm amber sun over water and distant modern city skyline, charcoal blue evening sky, copper reflections, subtle railings leading into distance. Left half is quiet dark skyline and sky with low detail and deep shadows, usable negative space behind native white title text. Runner is a small scene subject on right and occupies about 45 percent of image height, natural athletic pose, anonymous face not visible. Premium warm evening mood, genuine photographic texture. This is only a scene photograph, no phone, no UI, no text, no logos, no numbers, no watermark. Wide framing, avoid close-up or centered subject.

与用户参考图保持暖色、城市、水面、背影跑者的氛围，人物与城市构图不同。所有交互、数字、按钮和玻璃层仍由SwiftUI实时绘制。
