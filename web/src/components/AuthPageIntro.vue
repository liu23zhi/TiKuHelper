<template>
  <aside class="hero auth-intro">
    <div class="hero-badge">{{ eyebrow }}</div>
    <h1>{{ title }}</h1>
    <p>{{ description }}</p>
    <slot />
  </aside>
</template>

<script>
export default {
  name: 'AuthPageIntro',
  props: {
    eyebrow: { type: String, required: true },
    title: { type: String, required: true },
    description: { type: String, required: true },
  },
}
</script>
