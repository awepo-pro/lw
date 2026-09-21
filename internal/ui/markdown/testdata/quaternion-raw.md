$$
q = q_{0} + q_{1} \mathbf{i} + q_{2} \mathbf{j} + q_{3} \mathbf{k} \mathbf{i}^{2} = \mathbf{j}^{2} = \mathbf{k}^{2} = \mathbf{i} \mathbf{j} \mathbf{k} = - 1
$$
 
$$
\begin{aligned}
q = q_0 + q_1\mathbf{i} + q_2\mathbf{j} + q_3\mathbf{k}
  \\
  \mathbf{i}^2 = \mathbf{j}^2 = \mathbf{k}^2 = \mathbf{ijk}=-1
\end{aligned}
$$
 其他關於四元數的運算細節請參考此 [文章](https://krasjet.github.io/quaternion/quaternion.pdf) \[1\]。

#### 四元數與三維旋轉

我們可以用一個單位四元數 unit quaternion （norm 為 1 的四元數）來表示三維空間中的旋轉。我們假設空間中的點 p=\[x,y,z\] $p=[x,y,z]$ $p = [x, y, z]$ 繞著旋轉軸 u $u$ $\mathbf{u}$ 旋轉 θ $θ$ $\theta$ 度。在計算的時候我們把此三維空間中的點用一個虛四元數來表示： v=\[0,x,y,z\] 
$$
v=[0,x,y,z]
$$
 
$$
v = [0, x, y, z]
$$
 並且把旋轉軸 u $u$ $\mathbf{u}$ 及旋轉角度 θ $θ$ $\theta$ 用一個四元數 q $q$ $q$ 來表示： q=\[cos(12θ),sin(12θ)u\] 
$$
q = \left[c o s \left(\frac{1}{2} \theta\right) , s i n \left(\frac{1}{2} \theta\right) \mathbf{u}\right]
$$
 
$$
q = [cos(\frac{1}{2}\theta), sin(\frac{1}{2}\theta)\mathbf{u}]
$$
 則旋轉過後的點 p′=\[x′,y′,z′ $p^{'} = \left[\right. x^{'} , y^{'} , z^{'}$ $p' = [x', y', z'$ 可以表示為： v′=\[0,x′,y′,z′\]=qvq−1 
$$
v^{'} = \left[0 , x^{'} , y^{'} , z^{'}\right] = q v q^{- 1}
$$
 
$$
v' = [0, x', y', z'] = qvq^{-1}
$$
 公式的證明細節也請參閱參考資料 \[1\]。

### 四元數與旋轉向量轉換的例子

上面提到將旋轉向量 \[u,θ\] $[u,θ]$ $[\mathbf{u}, \theta]$ 轉換成四元數可以用此公式 q=\[cos(12θ),sin(12θ)u\] $q = \left[c o s \left(\frac{1}{2} \theta\right) , s i n \left(\frac{1}{2} \theta\right) \mathbf{u}\right]$ $q = [cos(\frac{1}{2}\theta), sin(\frac{1}{2}\theta)\mathbf{u}]$ ，而將一個四元數 q=\[q0,q1,q2,q3\] $q = \left[q_{0} , q_{1} , q_{2} , q_{3}\right]$ $q = [q_0, q_1, q_2, q_3]$ 轉換成旋轉向量則可用以下公式： θ=2 acos(q0)u=\[q1,q2,q3\]/sin(θ2) 
$$
\theta = 2 a c o s \left(q_{0}\right) \mathbf{u} = \left[q_{1} , q_{2} , q_{3}\right] / s i n \left(\frac{\theta}{2}\right)
$$
 
$$
\begin{aligned}
\theta = 2\ acos(q_0)
  \\
  \mathbf{u} = [q_1, q_2, q_3] / sin(\frac{\theta}{2})
\end{aligned}
$$
 打個比方，假設三維空間中的旋轉軸為 \[1√3,1√3,1√3\] $\left[\frac{1}{\sqrt{3}} , \frac{1}{\sqrt{3}} , \frac{1}{\sqrt{3}}\right]$ $[\frac{1}{\sqrt{3}}, \frac{1}{\sqrt{3}}, \frac{1}{\sqrt{3}}]$ ，而旋轉的角度為 120 度，也就是 23π $\frac{2}{3} \pi$ $\frac{2}{3}\pi$ ，則轉換後的四元數為： q=\[cos(12θ),sin(12θ)u\]=\[cos(13π),sin(13π)(1√3i,1√3j,1√3k)\]=\[12,12i,12j,12k\] 
