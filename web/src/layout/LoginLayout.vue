<template>
  <Header/>
  <main>
    <router-view></router-view>
  </main>
  <Footer/>
</template>

<script>
import Header from '../components/PageHeader'
import Footer from '../components/PageFoot'
import { useRouter } from 'vue-router';
import { getCookies } from '../utils/cookies'
import { getStoredCurrentUser, resolveHomePath } from '../utils/currentUser'
export default {
  name: 'LoginLayout',
  setup(){
    const router = useRouter()
    const token = getCookies('token')
    const currentUser = getStoredCurrentUser()
    if(token){
      router.push(resolveHomePath(currentUser))
    }
  },
  components:{
    Header,
    Footer
  }
}
</script>

<style scoped>
main{
  width: 100%;
  height: calc(100% - 120px);
  display: flex;
  align-items: center;
  justify-content: center;
}
</style>
